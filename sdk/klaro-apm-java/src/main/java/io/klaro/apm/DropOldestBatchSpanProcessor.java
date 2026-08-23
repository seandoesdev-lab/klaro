package io.klaro.apm;

import io.opentelemetry.context.Context;
import io.opentelemetry.sdk.common.CompletableResultCode;
import io.opentelemetry.sdk.trace.ReadWriteSpan;
import io.opentelemetry.sdk.trace.ReadableSpan;
import io.opentelemetry.sdk.trace.SpanProcessor;
import io.opentelemetry.sdk.trace.data.SpanData;
import io.opentelemetry.sdk.trace.export.SpanExporter;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.List;
import java.util.logging.Level;
import java.util.logging.Logger;

/**
 * klaro-apm 전용 최소 구현: 큐 포화 시 drop-oldest 배치 프로세서.
 *
 * <p>thin wrapper 원칙(HOW-11)에 따라 OTel 공식 SDK가 이미 제공하는 동작은 재구현하지 않는다.
 * 하지만 OTel Java의 표준 {@code BatchSpanProcessor}는 큐가 가득 찼을 때 새로 들어오는 span을
 * 버리는(drop-newest) 동작이다 - {@code Worker.addSpan()}이 고정 크기 큐에 {@code offer()}만
 * 시도하고 실패하면 새 span을 그냥 버린다(SDK_CONTRACT.md 5가 요구하는 drop-oldest와 반대).
 * Node와 마찬가지로 OTel Java에도 동등한 표준 컴포넌트가 없어, 5의 "없는 경우에만 최소 구현을
 * 추가한다" 조항에 따라 이 클래스를 추가한다(SDK_CONTRACT.md 10/11).
 *
 * <p>실제 네트워크 전송과 실패 판정은 전적으로 주입된 {@link SpanExporter}(OTel
 * {@code OtlpGrpcSpanExporter})에 위임한다 - 재시도 로직이나 자체 프로토콜을 추가하지 않는다.
 * {@code onEnd}는 동기화된 큐에 push만 하고(빠른 O(1) 연산) 실제 export는 항상 별도의 데몬
 * 스레드에서 수행해 호출 스레드를 절대 막지 않는다.
 */
public final class DropOldestBatchSpanProcessor implements SpanProcessor {

  private static final Logger logger = Logger.getLogger(DropOldestBatchSpanProcessor.class.getName());

  private static final int DEFAULT_MAX_QUEUE_SIZE = 2048;
  private static final int DEFAULT_MAX_EXPORT_BATCH_SIZE = 512;
  private static final long DEFAULT_SCHEDULE_DELAY_MILLIS = 5000;

  private final SpanExporter spanExporter;
  private final int maxQueueSize;
  private final int maxExportBatchSize;
  private final long scheduleDelayMillis;

  private final ArrayDeque<SpanData> queue = new ArrayDeque<>();
  private final Object lock = new Object();
  private final Thread workerThread;
  private volatile boolean shutdown = false;
  private long droppedCount = 0;

  public static Builder builder(SpanExporter spanExporter) {
    return new Builder(spanExporter);
  }

  private DropOldestBatchSpanProcessor(
      SpanExporter spanExporter, int maxQueueSize, int maxExportBatchSize, long scheduleDelayMillis) {
    this.spanExporter = spanExporter;
    this.maxQueueSize = maxQueueSize;
    this.maxExportBatchSize = maxExportBatchSize;
    this.scheduleDelayMillis = scheduleDelayMillis;
    this.workerThread = new Thread(this::runLoop, "klaro-apm-drop-oldest-batch-span-processor");
    this.workerThread.setDaemon(true);
    this.workerThread.start();
  }

  @Override
  public void onStart(Context parentContext, ReadWriteSpan span) {
    // no-op: 시작 시점에는 export할 데이터가 없다.
  }

  @Override
  public boolean isStartRequired() {
    return false;
  }

  @Override
  public void onEnd(ReadableSpan span) {
    if (shutdown) {
      return;
    }
    SpanData data = span.toSpanData();
    synchronized (lock) {
      if (queue.size() >= maxQueueSize) {
        // 포화 시 가장 오래된 항목을 버린다(drop-oldest) - SDK_CONTRACT.md 5.
        queue.pollFirst();
        droppedCount++;
      }
      queue.addLast(data);
      if (droppedCount > 0) {
        logger.warning("klaro-apm: dropped " + droppedCount + " oldest span(s) because maxQueueSize was reached");
        droppedCount = 0;
      }
      lock.notifyAll();
    }
  }

  @Override
  public boolean isEndRequired() {
    return true;
  }

  private void runLoop() {
    while (!shutdown) {
      List<SpanData> batch = takeBatch();
      if (!batch.isEmpty()) {
        exportBatch(batch);
      }
    }
  }

  private List<SpanData> takeBatch() {
    synchronized (lock) {
      long deadline = System.currentTimeMillis() + scheduleDelayMillis;
      while (queue.size() < maxExportBatchSize && !shutdown) {
        long remaining = deadline - System.currentTimeMillis();
        if (remaining <= 0) {
          break;
        }
        try {
          lock.wait(remaining);
        } catch (InterruptedException e) {
          Thread.currentThread().interrupt();
          break;
        }
      }
      return drainLocked();
    }
  }

  /** 호출자가 {@code lock}을 이미 보유하고 있어야 한다. */
  private List<SpanData> drainLocked() {
    int n = Math.min(queue.size(), maxExportBatchSize);
    List<SpanData> batch = new ArrayList<>(n);
    for (int i = 0; i < n; i++) {
      batch.add(queue.pollFirst());
    }
    return batch;
  }

  private void exportBatch(List<SpanData> batch) {
    try {
      CompletableResultCode result = spanExporter.export(batch);
      result.join(30, java.util.concurrent.TimeUnit.SECONDS);
      if (!result.isSuccess()) {
        // Collector 연결 실패는 조용히 기록만 한다 - 앱에 예외를 전파하지 않는다(APM-01).
        // 실패한 배치는 재큐잉하지 않는다: 다음 배치 주기에 새로 쌓인 span으로 자연스럽게
        // 이어진다(OTel BatchSpanProcessor와 동일한 시맨틱).
        logger.fine("klaro-apm: span export failed, will retry on next batch cycle");
      }
    } catch (Throwable t) {
      // export()는 스펙상 예외를 던지지 않아야 하지만, 방어적으로 한 번 더 억제한다.
      logger.log(Level.FINE, "klaro-apm: span export threw, suppressing", t);
    }
  }

  @Override
  public CompletableResultCode forceFlush() {
    List<SpanData> all;
    synchronized (lock) {
      all = new ArrayList<>(queue);
      queue.clear();
      lock.notifyAll();
    }

    List<CompletableResultCode> results = new ArrayList<>();
    for (int i = 0; i < all.size(); i += maxExportBatchSize) {
      List<SpanData> chunk = all.subList(i, Math.min(i + maxExportBatchSize, all.size()));
      results.add(exportChunk(chunk));
    }
    return CompletableResultCode.ofAll(results);
  }

  private CompletableResultCode exportChunk(List<SpanData> chunk) {
    try {
      return spanExporter.export(new ArrayList<>(chunk));
    } catch (Throwable t) {
      logger.log(Level.FINE, "klaro-apm: span export threw, suppressing", t);
      return CompletableResultCode.ofFailure();
    }
  }

  @Override
  public CompletableResultCode shutdown() {
    if (shutdown) {
      return CompletableResultCode.ofSuccess();
    }
    shutdown = true;
    synchronized (lock) {
      lock.notifyAll();
    }

    CompletableResultCode flushResult = forceFlush();
    try {
      workerThread.join(5000);
    } catch (InterruptedException e) {
      Thread.currentThread().interrupt();
    }

    CompletableResultCode exporterShutdown;
    try {
      exporterShutdown = spanExporter.shutdown();
    } catch (Throwable t) {
      logger.log(Level.FINE, "klaro-apm: exporter shutdown threw, suppressing", t);
      exporterShutdown = CompletableResultCode.ofFailure();
    }
    return CompletableResultCode.ofAll(List.of(flushResult, exporterShutdown));
  }

  /** {@link DropOldestBatchSpanProcessor} 빌더. */
  public static final class Builder {
    private final SpanExporter spanExporter;
    private int maxQueueSize = DEFAULT_MAX_QUEUE_SIZE;
    private int maxExportBatchSize = DEFAULT_MAX_EXPORT_BATCH_SIZE;
    private long scheduleDelayMillis = DEFAULT_SCHEDULE_DELAY_MILLIS;

    private Builder(SpanExporter spanExporter) {
      this.spanExporter = spanExporter;
    }

    public Builder setMaxQueueSize(int maxQueueSize) {
      this.maxQueueSize = maxQueueSize;
      return this;
    }

    public Builder setMaxExportBatchSize(int maxExportBatchSize) {
      this.maxExportBatchSize = maxExportBatchSize;
      return this;
    }

    public Builder setScheduleDelayMillis(long scheduleDelayMillis) {
      this.scheduleDelayMillis = scheduleDelayMillis;
      return this;
    }

    public DropOldestBatchSpanProcessor build() {
      return new DropOldestBatchSpanProcessor(spanExporter, maxQueueSize, maxExportBatchSize, scheduleDelayMillis);
    }
  }
}
