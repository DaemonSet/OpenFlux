package io.openflux.app;

import java.io.DataInputStream;
import java.io.IOException;
import java.io.OutputStream;
import java.net.Inet4Address;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.Socket;

final class Socks5Client {
    private Socks5Client() {
    }

    static Socket connect(
            int proxyPort,
            InetSocketAddress target,
            int timeoutMs
    ) throws IOException {

        InetAddress address = target.getAddress();

        if (!(address instanceof Inet4Address)) {
            throw new IOException(
                    "SOCKS5 IPv4 target required: " + target
            );
        }

        byte[] ip = address.getAddress();

        Socket socket = new Socket();

        try {
            socket.connect(
                    new InetSocketAddress("127.0.0.1", proxyPort),
                    timeoutMs
            );

            socket.setSoTimeout(timeoutMs);

            OutputStream output = socket.getOutputStream();
            DataInputStream input =
                    new DataInputStream(socket.getInputStream());

            // SOCKS5, one auth method, no-auth.
            output.write(new byte[] {
                    0x05, 0x01, 0x00
            });
            output.flush();

            byte[] authReply = new byte[2];
            input.readFully(authReply);

            if (authReply[0] != 0x05 || authReply[1] != 0x00) {
                throw new IOException(
                        "SOCKS5 refused no-auth"
                );
            }

            int port = target.getPort();

            // OpenFlux SOCKS implementation expects this request
            // as one complete write.
            output.write(new byte[] {
                    0x05,
                    0x01,
                    0x00,
                    0x01,
                    ip[0],
                    ip[1],
                    ip[2],
                    ip[3],
                    (byte) (port >>> 8),
                    (byte) port
            });

            output.flush();

            byte[] reply = new byte[10];
            input.readFully(reply);

            if (reply[0] != 0x05 || reply[1] != 0x00) {
                throw new IOException(
                        "SOCKS5 CONNECT failed, reply="
                                + (reply[1] & 0xff)
                );
            }

            socket.setSoTimeout(0);

            return socket;

        } catch (IOException error) {
            try {
                socket.close();
            } catch (IOException ignored) {
            }

            throw error;
        }
    }
}
