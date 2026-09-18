package io.openflux.app;

import android.util.Log;

final class NativeBridge {
    private static final String TAG = "NativeBridge";
    private static volatile boolean loaded;

    private NativeBridge() {
    }

    static synchronized void ensureLoaded() {
        if (loaded) return;

        System.loadLibrary("system");
        loaded = true;

        Log.i(TAG, "libsystem JNI bridge loaded");
    }

    static native int sendfd(int fd, String socketPath);

    static native void jniclose(int fd);
}
