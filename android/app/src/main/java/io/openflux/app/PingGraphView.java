package io.openflux.app;

import android.animation.ValueAnimator;
import android.content.Context;
import android.graphics.Canvas;
import android.graphics.Color;
import android.graphics.Paint;
import android.graphics.Path;
import android.view.View;
import android.view.animation.DecelerateInterpolator;

import java.util.ArrayList;
import java.util.List;

final class PingGraphView extends View {
    private static final int MAX_SAMPLES = 32;

    private final ArrayList<Float> samples = new ArrayList<>();
    private final Paint gridPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint fillPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint linePaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final Paint dotPaint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private ValueAnimator animator;

    PingGraphView(Context context, int lineColor, int gridColor) {
        super(context);
        gridPaint.setColor(gridColor);
        gridPaint.setStrokeWidth(dp(1));
        gridPaint.setAlpha(90);
        linePaint.setColor(lineColor);
        linePaint.setStyle(Paint.Style.STROKE);
        linePaint.setStrokeWidth(dp(2.2f));
        linePaint.setStrokeCap(Paint.Cap.ROUND);
        linePaint.setStrokeJoin(Paint.Join.ROUND);
        fillPaint.setColor(lineColor);
        fillPaint.setStyle(Paint.Style.FILL);
        fillPaint.setAlpha(28);
        dotPaint.setColor(lineColor);
        setLayerType(View.LAYER_TYPE_SOFTWARE, null);
    }

    void setHistory(List<Float> history) {
        samples.clear();
        int start = Math.max(0, history.size() - MAX_SAMPLES);
        samples.addAll(history.subList(start, history.size()));
        invalidate();
    }

    void addSample(float milliseconds) {
        float target = Math.max(1f, milliseconds);
        float start = samples.isEmpty() ? target : samples.get(samples.size() - 1);
        if (samples.size() == MAX_SAMPLES) samples.remove(0);
        samples.add(start);
        int index = samples.size() - 1;
        if (animator != null) animator.cancel();
        animator = ValueAnimator.ofFloat(start, target);
        animator.setDuration(420);
        animator.setInterpolator(new DecelerateInterpolator());
        animator.addUpdateListener(animation -> {
            if (index < samples.size()) {
                samples.set(index, (float) animation.getAnimatedValue());
                invalidate();
            }
        });
        animator.start();
    }

    @Override protected void onDraw(Canvas canvas) {
        super.onDraw(canvas);
        int width = getWidth();
        int height = getHeight();
        if (width <= 0 || height <= 0) return;

        float top = dp(5);
        float bottom = height - dp(5);
        canvas.drawLine(0, top, width, top, gridPaint);
        canvas.drawLine(0, (top + bottom) / 2f, width, (top + bottom) / 2f, gridPaint);
        canvas.drawLine(0, bottom, width, bottom, gridPaint);
        if (samples.isEmpty()) return;

        float min = samples.get(0);
        float max = min;
        for (float value : samples) {
            min = Math.min(min, value);
            max = Math.max(max, value);
        }
        float padding = Math.max(8f, (max - min) * 0.25f);
        min = Math.max(0f, min - padding);
        max += padding;

        Path line = new Path();
        float previousX = 0f;
        float previousY = yFor(samples.get(0), min, max, top, bottom);
        line.moveTo(previousX, previousY);
        float spacing = samples.size() <= 1 ? width : width / (float) (MAX_SAMPLES - 1);
        float offset = width - spacing * (samples.size() - 1);
        previousX = offset;
        line.reset();
        line.moveTo(previousX, previousY);
        for (int index = 1; index < samples.size(); index++) {
            float x = offset + spacing * index;
            float y = yFor(samples.get(index), min, max, top, bottom);
            float middle = (previousX + x) / 2f;
            line.cubicTo(middle, previousY, middle, y, x, y);
            previousX = x;
            previousY = y;
        }

        Path area = new Path(line);
        area.lineTo(previousX, bottom);
        area.lineTo(offset, bottom);
        area.close();
        canvas.drawPath(area, fillPaint);
        canvas.drawPath(line, linePaint);
        canvas.drawCircle(previousX, previousY, dp(3.2f), dotPaint);
    }

    private float yFor(float value, float min, float max, float top, float bottom) {
        if (max <= min) return (top + bottom) / 2f;
        return bottom - ((value - min) / (max - min)) * (bottom - top);
    }

    private float dp(float value) {
        return value * getResources().getDisplayMetrics().density;
    }
}
