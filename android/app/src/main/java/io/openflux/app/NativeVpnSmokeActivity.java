package io.openflux.app;

import android.app.Activity;
import android.content.Intent;
import android.net.VpnService;
import android.os.Bundle;
import android.widget.TextView;

public final class NativeVpnSmokeActivity extends Activity {
    private static final int VPN_REQUEST = 1001;

    private TextView status;

    @Override
    protected void onCreate(Bundle state) {
        super.onCreate(state);

        status = new TextView(this);
        status.setTextSize(18);
        status.setPadding(48, 48, 48, 48);
        status.setText("Preparing native VPN...");
        setContentView(status);

        requestVpn();
    }

    private void requestVpn() {
        Intent prepare = VpnService.prepare(this);

        if (prepare != null) {
            status.setText("Waiting for Android VPN permission...");
            startActivityForResult(prepare, VPN_REQUEST);
        } else {
            startNativeVpn();
        }
    }

    @Override
    protected void onActivityResult(
            int requestCode,
            int resultCode,
            Intent data
    ) {
        super.onActivityResult(
                requestCode,
                resultCode,
                data
        );

        if (requestCode != VPN_REQUEST) {
            return;
        }

        if (resultCode == RESULT_OK) {
            startNativeVpn();
        } else {
            status.setText("VPN permission denied.");
        }
    }

    private void startNativeVpn() {
        String transport =
                getIntent().getStringExtra("transport");

        if (!"mailru".equalsIgnoreCase(transport)) {
            transport = "volga";
        }

        Intent intent =
                new Intent(
                        this,
                        NativeVpnSmokeService.class
                );

        intent.setAction(
                NativeVpnSmokeService.ACTION_START
        );

        intent.putExtra(
                "transport",
                transport
        );

        startForegroundService(intent);

        status.setText(
                "Native VPN start requested.\n\n"
                        + "Transport: "
                        + transport
                        + "\n\n"
                        + "Check logcat."
        );
    }
}
