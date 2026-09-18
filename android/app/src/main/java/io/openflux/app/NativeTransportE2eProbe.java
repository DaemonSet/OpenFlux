package io.openflux.app;

import java.io.BufferedReader;
import java.io.IOException;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.Socket;
import java.nio.charset.StandardCharsets;

/*
 * End-to-end transport probe for the native smoke harness.
 *
 * This deliberately enters through OpenFlux's local SOCKS5 listener:
 *
 *   app -> localhost SOCKS5 -> OpenFlux -> carrier -> exit-node -> Internet
 *
 * The OpenFlux app UID is excluded from Android's VPN to prevent carrier
 * recursion, so this probe verifies the native transport path rather than
 * claiming that the probe itself traversed the Android TUN.
 */
final class NativeTransportE2eProbe {
    private static final byte[] TARGET_IPV4 =
            new byte[] {1, 1, 1, 1};

    private static final int TARGET_PORT = 80;

    private NativeTransportE2eProbe() {
    }

    static String probeHttp(
            int socksPort,
            int timeoutMs
    ) throws IOException {
        InetAddress targetAddress =
                InetAddress.getByAddress(TARGET_IPV4);

        InetSocketAddress target =
                new InetSocketAddress(
                        targetAddress,
                        TARGET_PORT
                );

        try (Socket socket = Socks5Client.connect(
                socksPort,
                target,
                timeoutMs
        )) {
            socket.setSoTimeout(timeoutMs);

            OutputStream output =
                    socket.getOutputStream();

            output.write((
                    "HEAD / HTTP/1.1\r\n"
                            + "Host: 1.1.1.1\r\n"
                            + "Connection: close\r\n"
                            + "\r\n"
            ).getBytes(StandardCharsets.US_ASCII));

            output.flush();

            BufferedReader reader =
                    new BufferedReader(
                            new InputStreamReader(
                                    socket.getInputStream(),
                                    StandardCharsets.US_ASCII
                            )
                    );

            String statusLine = reader.readLine();

            if (statusLine == null
                    || !statusLine.startsWith("HTTP/")) {
                throw new IOException(
                        "invalid HTTP response through OpenFlux"
                );
            }

            return statusLine;
        }
    }
}
