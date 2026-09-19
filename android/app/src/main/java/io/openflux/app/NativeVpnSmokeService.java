package io.openflux.app;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.net.VpnService;
import android.os.ParcelFileDescriptor;
import android.os.PowerManager;
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

    private static final int E2E_ATTEMPT_TIMEOUT_MS = 4_000;
    private static final long E2E_HEALTHY_ACTIVE_INTERVAL_MS = 15_000;
    private static final long E2E_HEALTHY_IDLE_INTERVAL_MS = 5 * 60_000;
    private static final long E2E_RETRY_MIN_MS = 500;
    private static final long E2E_RETRY_ACTIVE_MAX_MS = 8_000;
    private static final long E2E_RETRY_IDLE_MIN_MS = 8_000;
    private static final long E2E_RETRY_IDLE_ONE_MINUTE_MS = 60_000;
    private static final long E2E_RETRY_IDLE_MAX_MS = 5 * 60_000;
    private static final long E2E_RETRY_IDLE_SCREEN_CHECK_MS = 30_000;

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
                    "NATIVE VPN LOCAL READY (transport E2E probe pending)");

            updateNotification(
                    "Local dataplane ready; checking transport E2E"
            );

            monitorTransportE2e(
                    session,
                    socksPort
            );

        } catch (Throwable error) {
            if (!isCurrent(session)) {
                return;
            }

            Log.e(TAG,
                    "Native VPN failed",
                    error);

            fail(
                    error.getClass().getSimpleName()
                            + ": "
                            + String.valueOf(
                                    error.getMessage()
                            )
            );
        }
    }

    private void monitorTransportE2e(
            int session,
            int socksPort
    ) throws InterruptedException {
        long retryDelayMs = E2E_RETRY_MIN_MS;
        int failedAttempts = 0;
        boolean everReady = false;
        boolean ready = false;

        while (isCurrent(session)) {
            if (ready
                    && !sleepForSession(
                            session,
                            healthyE2eIntervalMs()
                    )) {
                return;
            }

            try {
                String statusLine =
                        NativeTransportE2eProbe.probeHttp(
                                socksPort,
                                E2E_ATTEMPT_TIMEOUT_MS
                        );

                if (!isCurrent(session)) {
                    return;
                }

                if (!everReady) {
                    Log.i(TAG,
                            "NATIVE TRANSPORT E2E READY via SOCKS5: "
                                    + statusLine);

                    updateNotification(
                            "Native transport E2E verified"
                    );

                } else if (!ready) {
                    Log.i(TAG,
                            "NATIVE TRANSPORT E2E RESTORED via SOCKS5: "
                                    + statusLine);

                    updateNotification(
                            "Native transport E2E restored"
                    );
                }

                everReady = true;
                ready = true;
                failedAttempts = 0;
                retryDelayMs = E2E_RETRY_MIN_MS;

            } catch (IOException error) {
                if (!isCurrent(session)) {
                    return;
                }

                failedAttempts++;

                if (ready) {
                    ready = false;

                    Log.w(TAG,
                            "NATIVE TRANSPORT E2E DEGRADED: "
                                    + String.valueOf(
                                            error.getMessage()
                                    ));

                    updateNotification(
                            "Native transport degraded; recovering"
                    );

                } else {
                    String phase =
                            everReady
                                    ? "recovery"
                                    : "startup";

                    Log.w(TAG,
                            "Transport E2E "
                                    + phase
                                    + " probe attempt "
                                    + failedAttempts
                                    + " failed: "
                                    + String.valueOf(
                                            error.getMessage()
                                    ));

                    if (!everReady) {
                        updateNotification(
                                "Local dataplane ready; waiting for transport E2E"
                        );
                    }
                }

                long retrySleepMs =
                        recoveryRetryDelayMs(
                                retryDelayMs
                        );

                if (!sleepForRecoverySession(
                        session,
                        retrySleepMs
                )) {
                    return;
                }

                retryDelayMs =
                        nextRecoveryRetryDelayMs(
                                retrySleepMs
                        );
            }
        }
    }

    private boolean isInteractive() {
        PowerManager powerManager =
                getSystemService(PowerManager.class);

        return powerManager == null
                || powerManager.isInteractive();
    }

    private long healthyE2eIntervalMs() {
        return isInteractive()
                ? E2E_HEALTHY_ACTIVE_INTERVAL_MS
                : E2E_HEALTHY_IDLE_INTERVAL_MS;
    }

    private long recoveryRetryDelayMs(
            long currentDelayMs
    ) {
        if (isInteractive()) {
            return Math.min(
                    currentDelayMs,
                    E2E_RETRY_ACTIVE_MAX_MS
            );
        }

        return Math.max(
                currentDelayMs,
                E2E_RETRY_IDLE_MIN_MS
        );
    }

    private long nextRecoveryRetryDelayMs(
            long currentDelayMs
    ) {
        if (isInteractive()) {
            return Math.min(
                    currentDelayMs * 2,
                    E2E_RETRY_ACTIVE_MAX_MS
            );
        }

        if (currentDelayMs
                < E2E_RETRY_IDLE_ONE_MINUTE_MS) {
            return Math.min(
                    currentDelayMs * 2,
                    E2E_RETRY_IDLE_ONE_MINUTE_MS
            );
        }

        return Math.min(
                currentDelayMs * 2,
                E2E_RETRY_IDLE_MAX_MS
        );
    }

    private boolean sleepForRecoverySession(
            int session,
            long delayMs
    ) throws InterruptedException {
        if (isInteractive()) {
            return sleepForSession(
                    session,
                    Math.min(
                            delayMs,
                            E2E_RETRY_ACTIVE_MAX_MS
                    )
            );
        }

        long remainingMs = delayMs;

        while (remainingMs > 0) {
            long sliceMs = Math.min(
                    remainingMs,
                    E2E_RETRY_IDLE_SCREEN_CHECK_MS
            );

            if (!sleepForSession(
                    session,
                    sliceMs
            )) {
                return false;
            }

            remainingMs -= sliceMs;

            /*
             * If the user wakes the screen while an idle backoff is
             * running, do not make them wait for the rest of a multi-minute
             * sleep. The next recovery probe starts immediately after this
             * bounded screen-state check.
             */
            if (isInteractive()) {
                return isCurrent(session);
            }
        }

        return isCurrent(session);
    }

    private boolean sleepForSession(
            int session,
            long delayMs
    ) throws InterruptedException {
        try {
            Thread.sleep(delayMs);
            return isCurrent(session);

        } catch (InterruptedException error) {
            if (!isCurrent(session)) {
                return false;
            }

            throw error;
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
