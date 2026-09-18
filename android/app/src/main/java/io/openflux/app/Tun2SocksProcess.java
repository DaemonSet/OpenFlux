package io.openflux.app;

import android.content.Context;
import android.util.Log;

import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.net.InetAddress;
import java.net.ServerSocket;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.TimeUnit;

final class Tun2SocksProcess {
    private static final String TAG = "Tun2Socks";

    private static final String NETIF_IP = "26.26.26.2";
    private static final String NETMASK = "255.255.255.0";
    private static final String DNS_GATEWAY = "26.26.26.1";

    private final Context context;

    private Process tun2socks;
    private Process pdnsd;
    private File socketPath;

    Tun2SocksProcess(Context context) {
        this.context = context.getApplicationContext();
    }

    boolean start(
            int tunFd,
            int socksPort,
            int nativeDnsPort,
            int mtu
    ) throws Exception {

        if (tunFd <= 0) {
            throw new IllegalArgumentException(
                    "Invalid TUN fd: " + tunFd
            );
        }

        if (socksPort <= 0) {
            throw new IllegalArgumentException(
                    "Invalid SOCKS port: " + socksPort
            );
        }

        NativeBridge.ensureLoaded();

        File nativeDir =
                new File(context.getApplicationInfo().nativeLibraryDir);

        File tunBinary =
                new File(nativeDir, "libtun2socks.so");

        File dnsBinary =
                new File(nativeDir, "libpdnsd.so");

        if (!tunBinary.isFile()) {
            throw new IOException(
                    "tun2socks not found: " + tunBinary
            );
        }

        if (!dnsBinary.isFile()) {
            throw new IOException(
                    "pdnsd not found: " + dnsBinary
            );
        }

        if (nativeDnsPort <= 0) {
            throw new IllegalArgumentException(
                    "Invalid native DNS port: " + nativeDnsPort
            );
        }

        int dnsPort = allocatePort();

        writePdnsdConfig(
                dnsPort,
                nativeDnsPort
        );

        Log.i(TAG,
                "pdnsd 26.26.26.1:"
                        + dnsPort
                        + " -> native DNSMux 127.0.0.1:"
                        + nativeDnsPort);

        pdnsd = new ProcessBuilder(
                dnsBinary.getAbsolutePath(),
                "-c",
                new File(
                        context.getFilesDir(),
                        "pdnsd.conf"
                ).getAbsolutePath()
        )
                .directory(context.getFilesDir())
                .redirectErrorStream(true)
                .start();

        startOutputPump(pdnsd, "PdnsdStdout");

        Thread.sleep(500);

        socketPath =
                new File(
                        context.getApplicationInfo().dataDir,
                        "openflux-tun.sock"
                );

        if (socketPath.exists()
                && !socketPath.delete()) {
            throw new IOException(
                    "Cannot remove stale socket "
                            + socketPath
            );
        }

        if (!socketPath.createNewFile()) {
            throw new IOException(
                    "Cannot create socket placeholder "
                            + socketPath
            );
        }

        List<String> command = new ArrayList<>();

        command.add(tunBinary.getAbsolutePath());

        command.add("--netif-ipaddr");
        command.add(NETIF_IP);

        command.add("--netif-netmask");
        command.add(NETMASK);

        command.add("--socks-server-addr");
        command.add("127.0.0.1:" + socksPort);

        command.add("--tunfd");
        command.add(Integer.toString(tunFd));

        command.add("--tunmtu");
        command.add(Integer.toString(mtu));

        command.add("--loglevel");
        command.add("3");

        command.add("--pid");
        command.add(
                new File(
                        context.getFilesDir(),
                        "tun2socks.pid"
                ).getAbsolutePath()
        );

        command.add("--sock");
        command.add(socketPath.getAbsolutePath());

        command.add("--dnsgw");
        command.add(
                DNS_GATEWAY + ":" + dnsPort
        );

        Log.i(TAG,
                "Starting tun2socks -> SOCKS5 127.0.0.1:"
                        + socksPort);

        tun2socks = new ProcessBuilder(command)
                .directory(context.getFilesDir())
                .redirectErrorStream(true)
                .start();

        startOutputPump(
                tun2socks,
                "Tun2SocksStdout"
        );

        Thread.sleep(500);

        for (int attempt = 1;
             attempt <= 10;
             attempt++) {

            /*
             * tun2socks daemonizes itself when --pid is used.
             * The ProcessBuilder parent therefore exits normally,
             * while the real daemon continues running and waits
             * for the TUN fd on the Unix socket.
             */
            int result = NativeBridge.sendfd(
                    tunFd,
                    socketPath.getAbsolutePath()
            );

            if (result == 0) {
                Log.i(TAG,
                        "TUN fd handoff OK, attempt "
                                + attempt);
                return true;
            }

            Log.w(TAG,
                    "TUN fd handoff failed, attempt "
                            + attempt
                            + ", result="
                            + result);

            Thread.sleep(500L * attempt);
        }

        throw new IOException(
                "Failed to pass TUN fd to tun2socks"
        );
    }

    void stop() {
        stopProcess(tun2socks);
        tun2socks = null;

        stopProcess(pdnsd);
        pdnsd = null;

        killPidFile(
                new File(
                        context.getFilesDir(),
                        "tun2socks.pid"
                )
        );

        killPidFile(
                new File(
                        context.getFilesDir(),
                        "pdnsd.pid"
                )
        );

        if (socketPath != null) {
            socketPath.delete();
            socketPath = null;
        }
    }

    private void writePdnsdConfig(
            int listenPort,
            int upstreamPort
    ) throws IOException {

        String config =
                context.getString(R.string.pdnsd_conf)
                        .replace(
                                "{DIR}",
                                context.getFilesDir().toString()
                        )
                        .replace(
                                "{LISTEN_IP}",
                                DNS_GATEWAY
                        )
                        .replace(
                                "{LISTEN_PORT}",
                                Integer.toString(listenPort)
                        )
                        .replace(
                                "{UPSTREAM_PORT}",
                                Integer.toString(upstreamPort)
                        );

        File configFile =
                new File(
                        context.getFilesDir(),
                        "pdnsd.conf"
                );

        try (FileOutputStream output =
                     new FileOutputStream(
                             configFile,
                             false
                     )) {

            output.write(
                    config.getBytes(
                            StandardCharsets.UTF_8
                    )
            );
        }

        File cache =
                new File(
                        context.getFilesDir(),
                        "pdnsd.cache"
                );

        if (!cache.exists()) {
            cache.createNewFile();
        }
    }

    private static int allocatePort()
            throws IOException {

        try (ServerSocket socket =
                     new ServerSocket(
                             0,
                             1,
                             InetAddress.getByName(
                                     "127.0.0.1"
                             )
                     )) {

            return socket.getLocalPort();
        }
    }

    private static void startOutputPump(
            Process process,
            String tag
    ) {

        Thread thread = new Thread(() -> {
            try (java.io.BufferedReader reader =
                         new java.io.BufferedReader(
                                 new java.io.InputStreamReader(
                                         process.getInputStream()
                                 )
                         )) {

                String line;

                while ((line =
                                reader.readLine())
                        != null) {

                    if (!line.isEmpty()) {
                        Log.d(tag, line);
                    }
                }

            } catch (IOException ignored) {
            }
        }, tag);

        thread.setDaemon(true);
        thread.start();
    }

    private static void stopProcess(
            Process process
    ) {

        if (process == null) return;

        process.destroy();

        try {
            if (!process.waitFor(
                    500,
                    TimeUnit.MILLISECONDS
            )) {
                process.destroyForcibly();
            }

        } catch (InterruptedException error) {
            Thread.currentThread().interrupt();
            process.destroyForcibly();
        }
    }

    private static void killPidFile(File file) {
        if (!file.isFile()) return;

        try {
            String value =
                    new String(
                            Files.readAllBytes(
                                    file.toPath()
                            ),
                            StandardCharsets.UTF_8
                    ).trim();

            int pid = Integer.parseInt(value);

            if (pid > 0) {
                android.os.Process.killProcess(pid);
            }

        } catch (Exception ignored) {
        }

        file.delete();
    }
}
