package io.openflux.app;

import android.util.Log;

import java.io.Closeable;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

final class DnsTcpRelay implements Closeable {
    private static final String TAG = "DnsTcpRelay";
    private static final int CONNECT_TIMEOUT_MS = 10_000;

    private final int socksPort;
    private final ServerSocket server;
    private final ExecutorService workers =
            Executors.newCachedThreadPool(r -> {
                Thread t = new Thread(r, "OpenFluxDnsRelay");
                t.setDaemon(true);
                return t;
            });

    private volatile boolean closed;

    DnsTcpRelay(int socksPort) throws IOException {
        this.socksPort = socksPort;

        server = new ServerSocket(
                0,
                50,
                InetAddress.getByAddress(
                        new byte[] {127, 0, 0, 1}
                )
        );
    }

    int getPort() {
        return server.getLocalPort();
    }

    void start() {
        workers.execute(this::acceptLoop);
    }

    private void acceptLoop() {
        while (!closed) {
            try {
                Socket client = server.accept();
                workers.execute(() -> serve(client));

            } catch (IOException error) {
                if (!closed) {
                    Log.w(TAG,
                            "accept failed: "
                                    + error.getMessage());
                }
                return;
            }
        }
    }

    private void serve(Socket client) {
        try (Socket local = client;
             Socket upstream = connectUpstream()) {

            Thread outgoing = new Thread(
                    () -> pump(local, upstream),
                    "OpenFluxDnsUp"
            );
            outgoing.setDaemon(true);
            outgoing.start();

            pump(upstream, local);

        } catch (IOException error) {
            if (!closed) {
                Log.w(TAG,
                        "DNS relay: "
                                + error.getMessage());
            }
        }
    }

    private Socket connectUpstream() throws IOException {
        IOException last = null;

        byte[][] resolvers = {
                {1, 1, 1, 1},
                {8, 8, 8, 8}
        };

        for (byte[] address : resolvers) {
            try {
                InetSocketAddress target =
                        new InetSocketAddress(
                                InetAddress.getByAddress(address),
                                53
                        );

                return Socks5Client.connect(
                        socksPort,
                        target,
                        CONNECT_TIMEOUT_MS
                );

            } catch (IOException error) {
                last = error;
            }
        }

        throw last != null
                ? last
                : new IOException("No DNS upstream");
    }

    private static void pump(Socket from, Socket to) {
        try {
            InputStream input = from.getInputStream();
            OutputStream output = to.getOutputStream();

            byte[] buffer = new byte[8192];
            int count;

            while ((count = input.read(buffer)) != -1) {
                output.write(buffer, 0, count);
                output.flush();
            }

        } catch (IOException ignored) {
        }

        try {
            to.shutdownOutput();
        } catch (IOException ignored) {
        }
    }

    @Override
    public void close() {
        closed = true;

        try {
            server.close();
        } catch (IOException ignored) {
        }

        workers.shutdownNow();
    }
}
