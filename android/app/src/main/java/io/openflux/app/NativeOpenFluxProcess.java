package io.openflux.app;

import android.content.Context;
import android.util.Log;

import java.io.BufferedReader;
import java.io.File;
import java.io.FileOutputStream;
import java.io.InputStreamReader;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.TimeUnit;

final class NativeOpenFluxProcess {
    private static final String TAG = "NativeOpenFlux";
    private static final long READY_TIMEOUT_MS = 45_000;
    private static final long READY_POLL_MS = 200;
    private static final long STOP_TIMEOUT_MS = 1_000;

    private final Context context;

    private Process process;
    private Thread outputThread;
    private File keyFile;
    private int socksPort;

    NativeOpenFluxProcess(Context context) {
        this.context = context.getApplicationContext();
    }

    synchronized boolean isRunning() {
        return process != null && process.isAlive();
    }

    synchronized int getSocksPort() {
        return socksPort;
    }

    /*
     * Starts our existing OpenFlux core as a native Android process:
     *
     *   localhost SOCKS5
     *       -> DNSMux
     *       -> OFM2
     *       -> compression
     *       -> AES-256-GCM
     *       -> Volga / Mail.ru
     *
     * This method is intended to run on a worker thread.
     */
    boolean start(String transport, String documentUrl, String encryptionSecret)
            throws Exception {

        synchronized (this) {
            if (isRunning()) {
                return true;
            }
        }

        File nativeDir = new File(context.getApplicationInfo().nativeLibraryDir);
        File binary = new File(nativeDir, "libopenflux_native.so");

        if (!binary.isFile()) {
            throw new IllegalStateException(
                    "Native OpenFlux binary not found: " + binary);
        }

        socksPort = allocateLoopbackPort();
        keyFile = writeKey(encryptionSecret);

        List<String> command = new ArrayList<>();
        command.add(binary.getAbsolutePath());

        command.add("--client");

        command.add("--transport");
        command.add(transport);

        command.add("--url");
        command.add(documentUrl);

        command.add("--socks5");
        command.add("127.0.0.1:" + socksPort);

        command.add("--encryption-key-file");
        command.add(keyFile.getAbsolutePath());

        Log.i(TAG, "Starting native OpenFlux");
        Log.i(TAG, "Transport: " + transport);
        Log.i(TAG, "SOCKS5: 127.0.0.1:" + socksPort);

        ProcessBuilder builder = new ProcessBuilder(command);
        builder.directory(context.getFilesDir());
        builder.redirectErrorStream(true);

        Process newProcess = builder.start();

        synchronized (this) {
            process = newProcess;
        }

        startOutputPump(newProcess);

        try {
            waitForSocks(newProcess, socksPort);
            deleteKey();

            Log.i(TAG, "Native OpenFlux ready on SOCKS5 port " + socksPort);
            return true;

        } catch (Exception error) {
            stop();
            throw error;
        }
    }

    void stop() {
        Process oldProcess;

        synchronized (this) {
            oldProcess = process;
            process = null;
            socksPort = 0;
        }

        if (oldProcess != null) {
            Log.i(TAG, "Stopping native OpenFlux");

            oldProcess.destroy();

            try {
                if (!oldProcess.waitFor(STOP_TIMEOUT_MS, TimeUnit.MILLISECONDS)) {
                    oldProcess.destroyForcibly();
                    oldProcess.waitFor(STOP_TIMEOUT_MS, TimeUnit.MILLISECONDS);
                }
            } catch (InterruptedException interrupted) {
                Thread.currentThread().interrupt();
                oldProcess.destroyForcibly();
            }
        }

        Thread oldOutput = outputThread;
        outputThread = null;

        if (oldOutput != null) {
            oldOutput.interrupt();
        }

        deleteKey();
    }

    private void waitForSocks(Process p, int port) throws Exception {
        long deadline = android.os.SystemClock.elapsedRealtime()
                + READY_TIMEOUT_MS;

        while (android.os.SystemClock.elapsedRealtime() < deadline) {
            if (!p.isAlive()) {
                throw new IllegalStateException(
                        "Native OpenFlux exited before SOCKS5 became ready"
                );
            }

            if (canConnect(port)) {
                return;
            }

            Thread.sleep(READY_POLL_MS);
        }

        throw new IllegalStateException(
                "Native OpenFlux SOCKS5 did not become ready within "
                        + (READY_TIMEOUT_MS / 1000)
                        + " seconds"
        );
    }

    private boolean canConnect(int port) {
        try (Socket socket = new Socket()) {
            socket.connect(
                    new InetSocketAddress(
                            InetAddress.getByName("127.0.0.1"),
                            port
                    ),
                    200
            );
            return true;
        } catch (Exception ignored) {
            return false;
        }
    }

    private int allocateLoopbackPort() throws Exception {
        try (ServerSocket socket = new ServerSocket(
                0,
                1,
                InetAddress.getByName("127.0.0.1")
        )) {
            return socket.getLocalPort();
        }
    }

    private File writeKey(String secret) throws Exception {
        if (secret == null
                || secret.getBytes(StandardCharsets.UTF_8).length < 16) {
            throw new IllegalArgumentException(
                    "Encryption secret must contain at least 16 bytes"
            );
        }

        File file = new File(
                context.getNoBackupFilesDir(),
                "openflux-native-encryption.key"
        );

        byte[] bytes = secret.trim()
                .getBytes(StandardCharsets.UTF_8);

        try (FileOutputStream output = new FileOutputStream(file, false)) {
            output.write(bytes);
            output.flush();
        }

        // Owner-only read permissions.
        file.setReadable(false, false);
        file.setWritable(false, false);
        file.setExecutable(false, false);

        if (!file.setReadable(true, true)) {
            throw new IllegalStateException(
                    "Cannot protect native encryption key"
            );
        }

        if (!file.setWritable(true, true)) {
            throw new IllegalStateException(
                    "Cannot protect native encryption key"
            );
        }

        return file;
    }

    private void deleteKey() {
        File file = keyFile;
        keyFile = null;

        if (file != null && file.exists() && !file.delete()) {
            Log.w(TAG, "Could not delete temporary encryption key");
        }
    }

    private void startOutputPump(Process p) {
        Thread thread = new Thread(() -> {
            try (BufferedReader reader = new BufferedReader(
                    new InputStreamReader(p.getInputStream())
            )) {
                String line;

                while ((line = reader.readLine()) != null) {
                    if (!line.isEmpty()) {
                        Log.d("NativeOpenFluxStdout", line);
                    }
                }
            } catch (Exception error) {
                if (p.isAlive()) {
                    Log.w(TAG, "Native output reader stopped", error);
                }
            }
        }, "NativeOpenFluxOutput");

        thread.setDaemon(true);
        thread.start();
        outputThread = thread;
    }
}
