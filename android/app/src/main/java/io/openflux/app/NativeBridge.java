package io.openflux.app;

import android.content.Context;
import android.os.Build;
import android.util.Log;

import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.util.zip.ZipEntry;
import java.util.zip.ZipFile;

final class NativeBridge {
    private static final String TAG = "NativeBridge";

    private static final String[] BUNDLED_LIBS = {
            "libtun2socks.so",
            "libpdnsd.so"
    };

    private static volatile boolean loaded;

    private NativeBridge() {
    }

    static synchronized void ensureLoaded(Context context) {
        if (loaded) return;

        try {
            for (String library : BUNDLED_LIBS) {
                extractIfNeeded(context, library);
            }

            System.loadLibrary("system");
            loaded = true;

            Log.i(TAG, "Native bridge loaded");

        } catch (Exception | UnsatisfiedLinkError error) {
            throw new RuntimeException(
                    "Failed to initialize native VPN bridge",
                    error
            );
        }
    }

    static File extractedLibrary(Context context, String name) {
        return new File(
                new File(context.getFilesDir(), "native"),
                name
        );
    }

    private static void extractIfNeeded(
            Context context,
            String library
    ) throws IOException {

        File directory =
                new File(context.getFilesDir(), "native");

        if (!directory.exists() && !directory.mkdirs()) {
            throw new IOException(
                    "Cannot create " + directory
            );
        }

        File output =
                new File(directory, library);

        if (output.exists()
                && output.length() > 0
                && output.canExecute()) {
            return;
        }

        String abi = Build.SUPPORTED_ABIS[0];

        String entryName =
                "lib/" + abi + "/" + library;

        try (ZipFile apk =
                     new ZipFile(context.getPackageCodePath())) {

            ZipEntry entry = apk.getEntry(entryName);

            if (entry == null) {
                throw new IOException(
                        "APK does not contain " + entryName
                );
            }

            try (InputStream input =
                         apk.getInputStream(entry);
                 FileOutputStream out =
                         new FileOutputStream(output, false)) {

                byte[] buffer = new byte[16 * 1024];
                int count;

                while ((count = input.read(buffer)) != -1) {
                    out.write(buffer, 0, count);
                }
            }
        }

        if (!output.setExecutable(true, true)) {
            throw new IOException(
                    "Cannot make executable: " + output
            );
        }

        Log.i(TAG,
                "Extracted "
                        + library
                        + " -> "
                        + output);
    }

    static native int sendfd(int fd, String socketPath);

    static native void jniclose(int fd);
}
