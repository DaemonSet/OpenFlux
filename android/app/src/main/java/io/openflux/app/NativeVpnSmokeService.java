package io.openflux.app;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.net.VpnService;
import android.os.ParcelFileDescriptor;
import android.util.Log;

import java.io.IOException;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.atomic.AtomicInteger;

public final class NativeVpnSmokeService extends VpnService {
    public static final String ACTION_START =
            "io.openflux.app.NATIVE_SMOKE_START";
    public static final String ACTION_STOP =
            "io.openflux.app.NATIVE_SMOKE_STOP";

    private static final String TAG = "NativeVpnSmoke";
    private static final String CHANNEL_ID =
            "openflux_native_smoke";
    private static final int NOTIFICATION_ID = 81;

    private final ExecutorService worker =
            Executors.newSingleThreadExecutor();

    private final AtomicInteger generation =
            new AtomicInteger();

    private volatile boolean active;

    private NativeOpenFluxProcess nativeOpenFlux;
    private Tun2SocksProcess tun2socks;
    private ParcelFileDescriptor tunnel;

    @Override
    public int onStartCommand(
            Intent intent,
            int flags,
            int startId
    ) {
        if (intent != null
                && ACTION_STOP.equals(intent.getAction())) {
            stopNativeVpn();
            return START_NOT_STICKY;
        }

        if (active) {
            return START_STICKY;
        }

        createChannel();
        startForeground(
                NOTIFICATION_ID,
                notification("Starting native VPN…")
        );

        SecureSettings settings =
                new SecureSettings(this);

        String url =
                settings.getString(
                        "document_url",
                        ""
                );

        String secret =
                settings.getString(
                        "encryption_secret",
                        ""
                );

        String transport =
                intent == null
                        ? null
                        : intent.getStringExtra("transport");

        if (!"mailru".equalsIgnoreCase(transport)) {
            transport = "volga";
        }

        if (url.trim().isEmpty()) {
            fail("Document URL is empty");
            return START_NOT_STICKY;
        }

        if (secret.trim().length() < 16) {
            fail("Encryption key is missing");
            return START_NOT_STICKY;
        }

        int mtu = 1400;

        active = true;
        int session = generation.incrementAndGet();

        String selectedTransport = transport;

        Log.i(TAG,
                "Starting native VPN, transport="
                        + selectedTransport);

        worker.execute(() ->
                startNativeVpn(
                        session,
                        selectedTransport,
                        url,
                        secret,
                        mtu
                )
        );

        return START_STICKY;
    }

    private void startNativeVpn(
            int session,
            String transport,
            String url,
            String secret,
            int mtu
    ) {
        try {
            if (!isCurrent(session)) return;

            /*
             * Stage 1:
             *
             * OpenFlux native:
             * SOCKS5 -> OFM2 -> compression ->
             * AES -> resilient carrier.
             */
            updateNotification(
                    "Connecting native OpenFlux…"
            );

            NativeOpenFluxProcess openFlux =
                    new NativeOpenFluxProcess(this);

            nativeOpenFlux = openFlux;

            openFlux.start(
                    transport,
                    url,
                    secret
            );

            if (!isCurrent(session)) {
                openFlux.stop();
                return;
            }

            int socksPort =
                    openFlux.getSocksPort();

            int dnsPort =
                    openFlux.getDnsPort();

            Log.i(TAG,
                    "OpenFlux SOCKS5 ready: 127.0.0.1:"
                            + socksPort);

            Log.i(TAG,
                    "OpenFlux DNSMux ready: 127.0.0.1:"
                            + dnsPort);

            /*
             * Stage 2:
             *
             * Establish Android TUN.
             *
             * The whole application UID is excluded so
             * OpenFlux's carrier connection does not
             * recurse back into its own VPN.
             */
            updateNotification(
                    "Creating Android TUN…"
            );

            Builder builder =
                    new Builder()
                            .setSession(
                                    "OpenFlux Native"
                            )
                            .setMtu(mtu)
                            .addAddress(
                                    "26.26.26.1",
                                    24
                            )
                            .addRoute(
                                    "0.0.0.0",
                                    0
                            )
                            .addDnsServer(
                                    "8.8.8.8"
                            );

            try {
                builder.addDisallowedApplication(
                        getPackageName()
                );
            } catch (PackageManager.NameNotFoundException error) {
                throw new IOException(
                        "Cannot exclude OpenFlux UID",
                        error
                );
            }

            ParcelFileDescriptor established =
                    builder.establish();

            if (established == null) {
                throw new IOException(
                        "Android failed to establish TUN"
                );
            }

            if (!isCurrent(session)) {
                established.close();
                return;
            }

            tunnel = established;

            Log.i(TAG,
                    "TUN established fd="
                            + established.getFd());

            /*
             * Stage 3:
             *
             * TUN FD
             *   -> native tun2socks
             *   -> localhost SOCKS5
             *   -> native OpenFlux.
             */
            updateNotification(
                    "Starting tun2socks…"
            );

            Tun2SocksProcess t2s =
                    new Tun2SocksProcess(this);

            tun2socks = t2s;

            t2s.start(
                    established.getFd(),
                    socksPort,
                    dnsPort,
                    mtu
            );

            if (!isCurrent(session)) {
                return;
            }

            Log.i(TAG,
                    "NATIVE VPN LOCAL READY (E2E not yet verified)");

            updateNotification(
                    "Native VPN local dataplane ready"
            );

        } catch (Throwable error) {
            Log.e(TAG,
                    "Native VPN failed",
                    error);

            if (isCurrent(session)) {
                fail(
                        error.getClass().getSimpleName()
                                + ": "
                                + String.valueOf(
                                        error.getMessage()
                                )
                );
            }
        }
    }

    private boolean isCurrent(int session) {
        return active
                && generation.get() == session;
    }

    private synchronized void fail(
            String message
    ) {
        Log.e(TAG, message);
        updateNotification(
                "ERROR: " + message
        );

        generation.incrementAndGet();
        active = false;

        cleanup();

        stopForeground(
                STOP_FOREGROUND_REMOVE
        );

        stopSelf();
    }

    private synchronized void stopNativeVpn() {
        Log.i(TAG, "Stopping native VPN");

        generation.incrementAndGet();
        active = false;

        cleanup();

        stopForeground(
                STOP_FOREGROUND_REMOVE
        );

        stopSelf();
    }

    private void cleanup() {
        Tun2SocksProcess localTun2Socks =
                tun2socks;
        tun2socks = null;

        if (localTun2Socks != null) {
            try {
                localTun2Socks.stop();
            } catch (Throwable error) {
                Log.w(TAG,
                        "tun2socks stop failed",
                        error);
            }
        }

        ParcelFileDescriptor localTunnel =
                tunnel;
        tunnel = null;

        if (localTunnel != null) {
            try {
                localTunnel.close();
            } catch (IOException ignored) {
            }
        }

        NativeOpenFluxProcess localOpenFlux =
                nativeOpenFlux;
        nativeOpenFlux = null;

        if (localOpenFlux != null) {
            try {
                localOpenFlux.stop();
            } catch (Throwable error) {
                Log.w(TAG,
                        "OpenFlux stop failed",
                        error);
            }
        }
    }

    @Override
    public void onDestroy() {
        generation.incrementAndGet();
        active = false;

        cleanup();

        worker.shutdownNow();

        super.onDestroy();
    }

    private void createChannel() {
        NotificationManager manager =
                getSystemService(
                        NotificationManager.class
                );

        if (manager == null) return;

        if (manager.getNotificationChannel(
                CHANNEL_ID
        ) == null) {
            NotificationChannel channel =
                    new NotificationChannel(
                            CHANNEL_ID,
                            "OpenFlux Native Test",
                            NotificationManager
                                    .IMPORTANCE_LOW
                    );

            manager.createNotificationChannel(
                    channel
            );
        }
    }

    private Notification notification(
            String text
    ) {
        Intent open =
                new Intent(
                        this,
                        MainActivity.class
                );

        PendingIntent pending =
                PendingIntent.getActivity(
                        this,
                        0,
                        open,
                        PendingIntent.FLAG_UPDATE_CURRENT
                                | PendingIntent.FLAG_IMMUTABLE
                );

        return new Notification.Builder(
                this,
                CHANNEL_ID
        )
                .setContentTitle(
                        "OpenFlux native test"
                )
                .setContentText(text)
                .setSmallIcon(
                        R.mipmap.ic_launcher
                )
                .setContentIntent(pending)
                .setOngoing(true)
                .build();
    }

    private void updateNotification(
            String text
    ) {
        NotificationManager manager =
                getSystemService(
                        NotificationManager.class
                );

        if (manager != null) {
            manager.notify(
                    NOTIFICATION_ID,
                    notification(text)
            );
        }
    }
}
