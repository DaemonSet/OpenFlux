package io.openflux.app;

import android.app.Activity;
import android.graphics.Color;
import android.os.Bundle;
import android.view.Gravity;
import android.widget.LinearLayout;
import android.widget.TextView;

import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

public final class NativeSmokeActivity extends Activity {
    private final ExecutorService worker =
            Executors.newSingleThreadExecutor();

    private NativeOpenFluxProcess nativeProcess;
    private TextView statusView;

    @Override
    protected void onCreate(Bundle state) {
        super.onCreate(state);

        buildUi();

        SecureSettings settings = new SecureSettings(this);

        String documentUrl =
                settings.getString("document_url", "");

        String encryptionSecret =
                settings.getString("encryption_secret", "");

        String transport =
                getIntent().getStringExtra("transport");

        if (!"mailru".equalsIgnoreCase(transport)) {
            transport = "volga";
        }

        if (documentUrl.trim().isEmpty()) {
            setStatus("ERROR\n\nDocument URL is not configured.");
            return;
        }

        if (encryptionSecret.trim()
                .getBytes(java.nio.charset.StandardCharsets.UTF_8)
                .length < 16) {
            setStatus("ERROR\n\nEncryption key is not configured.");
            return;
        }

        nativeProcess = new NativeOpenFluxProcess(this);

        final String selectedTransport = transport;

        setStatus(
                "Starting native OpenFlux...\n\n"
                        + "Transport: "
                        + selectedTransport
        );

        worker.execute(() -> {
            try {
                nativeProcess.start(
                        selectedTransport,
                        documentUrl,
                        encryptionSecret
                );

                int port = nativeProcess.getSocksPort();

                runOnUiThread(() ->
                        setStatus(
                                "NATIVE OPENFLUX READY\n\n"
                                        + "Transport: "
                                        + selectedTransport
                                        + "\n\nSOCKS5: 127.0.0.1:"
                                        + port
                                        + "\n\n"
                                        + "TUN is NOT connected yet.\n"
                                        + "This is only the native-core smoke test."
                        )
                );

            } catch (Throwable error) {
                runOnUiThread(() ->
                        setStatus(
                                "NATIVE OPENFLUX FAILED\n\n"
                                        + error.getClass().getSimpleName()
                                        + "\n\n"
                                        + String.valueOf(error.getMessage())
                        )
                );
            }
        });
    }

    private void buildUi() {
        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setGravity(Gravity.CENTER);
        root.setPadding(48, 48, 48, 48);
        root.setBackgroundColor(Color.rgb(18, 18, 18));

        statusView = new TextView(this);
        statusView.setTextColor(Color.WHITE);
        statusView.setTextSize(17);
        statusView.setGravity(Gravity.CENTER);
        statusView.setTextIsSelectable(true);

        root.addView(
                statusView,
                new LinearLayout.LayoutParams(
                        LinearLayout.LayoutParams.MATCH_PARENT,
                        LinearLayout.LayoutParams.WRAP_CONTENT
                )
        );

        setContentView(root);
    }

    private void setStatus(String text) {
        statusView.setText(text);
    }

    @Override
    protected void onDestroy() {
        if (nativeProcess != null) {
            nativeProcess.stop();
        }

        worker.shutdownNow();

        super.onDestroy();
    }
}
