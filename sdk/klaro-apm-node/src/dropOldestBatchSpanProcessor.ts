/**
 * klaro-apm 전용 최소 구현: 큐 포화 시 drop-oldest 배치 프로세서.
 *
 * thin wrapper 원칙(HOW-11)에 따라 OTel 공식 SDK가 이미 제공하는 동작은 재구현하지 않는다. 하지만
 * OTel JS의 표준 `BatchSpanProcessor`는 큐가 가득 찼을 때 **새로 들어오는 span을 버리는(drop-newest)**
 * 동작이다(SDK_CONTRACT.md §5가 요구하는 drop-oldest와 반대 — Python은 `deque(maxlen=N)`이 자연히
 * drop-oldest라서 재구현이 필요 없었지만 Node에는 그런 표준 컴포넌트가 없다). SDK_CONTRACT.md §5의
 * "없는 경우에만 최소 구현을 추가한다" 조항에 따라 이 클래스를 추가한다.
 *
 * 배치 스케줄링만 최소로 구현하고, 실제 네트워크 전송과 실패 판정은 전적으로 주입된
 * `SpanExporter.export()`(OTel `OTLPTraceExporter`)에 위임한다 — 재시도 로직이나 자체 프로토콜을
 * 추가하지 않는다. `onEnd`는 큐에 push만 하고 실제 flush는 항상 타이머(0ms 포함)로 미뤄 호출
 * 스레드를 절대 막지 않는다.
 */

import { diag } from '@opentelemetry/api';
import { ExportResultCode } from '@opentelemetry/core';
import type { ReadableSpan, SpanExporter, SpanProcessor } from '@opentelemetry/sdk-trace-base';

export interface DropOldestBatchSpanProcessorConfig {
  /** 큐 최대 크기. 초과 시 가장 오래된 항목을 버린다(drop-oldest). 기본값 2048. */
  maxQueueSize?: number;
  /** 배치 export 주기(ms). 기본값 5000. */
  scheduledDelayMillis?: number;
  /** 1회 export 배치 최대 span 수. 기본값 512. */
  maxExportBatchSize?: number;
}

const DEFAULT_MAX_QUEUE_SIZE = 2048;
const DEFAULT_SCHEDULED_DELAY_MILLIS = 5000;
const DEFAULT_MAX_EXPORT_BATCH_SIZE = 512;

export class DropOldestBatchSpanProcessor implements SpanProcessor {
  private readonly exporter: SpanExporter;
  private readonly maxQueueSize: number;
  private readonly scheduledDelayMillis: number;
  private readonly maxExportBatchSize: number;

  private queue: ReadableSpan[] = [];
  private timer: NodeJS.Timeout | undefined;
  private droppedCount = 0;
  private isFlushing = false;
  private isShutdown = false;

  constructor(exporter: SpanExporter, config: DropOldestBatchSpanProcessorConfig = {}) {
    this.exporter = exporter;
    this.maxQueueSize = config.maxQueueSize ?? DEFAULT_MAX_QUEUE_SIZE;
    this.scheduledDelayMillis = config.scheduledDelayMillis ?? DEFAULT_SCHEDULED_DELAY_MILLIS;
    this.maxExportBatchSize = config.maxExportBatchSize ?? DEFAULT_MAX_EXPORT_BATCH_SIZE;
  }

  onStart(): void {
    // no-op: 시작 시점에는 export할 데이터가 없다.
  }

  onEnd(span: ReadableSpan): void {
    if (this.isShutdown) return;

    if (this.queue.length >= this.maxQueueSize) {
      // 포화 시 가장 오래된 항목을 버린다(drop-oldest) — SDK_CONTRACT.md §5.
      this.queue.shift();
      this.droppedCount++;
    }
    this.queue.push(span);

    if (this.droppedCount > 0) {
      diag.warn(`klaro-apm: dropped ${this.droppedCount} oldest span(s) because maxQueueSize was reached`);
      this.droppedCount = 0;
    }

    this.scheduleFlushIfNeeded();
  }

  private scheduleFlushIfNeeded(): void {
    const delayMillis = this.queue.length >= this.maxExportBatchSize ? 0 : this.scheduledDelayMillis;

    if (this.timer) {
      if (delayMillis > 0) return; // 이미 예약된 flush가 있고 급하지 않으면 그대로 둔다.
      this.clearTimer();
    }

    this.timer = setTimeout(() => {
      this.timer = undefined;
      void this.runFlushLoop();
    }, delayMillis);
    this.timer.unref?.();
  }

  private async runFlushLoop(): Promise<void> {
    if (this.isFlushing) return;
    this.isFlushing = true;
    try {
      await this.flushOneBatch();
    } finally {
      this.isFlushing = false;
      if (this.queue.length > 0) {
        this.scheduleFlushIfNeeded();
      }
    }
  }

  private flushOneBatch(): Promise<void> {
    if (this.queue.length === 0) return Promise.resolve();

    const batch = this.queue.splice(0, this.maxExportBatchSize);
    return new Promise((resolve) => {
      try {
        this.exporter.export(batch, (result) => {
          if (result.code !== ExportResultCode.SUCCESS) {
            // Collector 연결 실패는 조용히 기록만 한다 — 고객 앱에 예외를 전파하지 않는다(APM-01).
            // 실패한 배치는 재큐잉하지 않는다: 다음 배치 주기에 새로 쌓인 span으로 자연스럽게
            // 이어진다(OTel BatchSpanProcessor와 동일한 시맨틱, SDK_CONTRACT.md §5).
            diag.debug('klaro-apm: span export failed', result.error);
          }
          resolve();
        });
      } catch (error) {
        // export()는 스펙상 예외를 던지지 않아야 하지만, 방어적으로 한 번 더 억제한다.
        diag.debug('klaro-apm: span export threw synchronously, suppressing', error);
        resolve();
      }
    });
  }

  async forceFlush(): Promise<void> {
    this.clearTimer();
    while (this.queue.length > 0) {
      await this.flushOneBatch();
    }
  }

  async shutdown(): Promise<void> {
    if (this.isShutdown) return;
    this.isShutdown = true;
    await this.forceFlush();
    await this.exporter.shutdown();
  }

  private clearTimer(): void {
    if (this.timer) {
      clearTimeout(this.timer);
      this.timer = undefined;
    }
  }
}
