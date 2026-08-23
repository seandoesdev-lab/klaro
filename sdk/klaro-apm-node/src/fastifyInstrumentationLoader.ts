/**
 * fastify.ts에서 분리된 얇은 로더 - 테스트에서 vi.mock으로 가로채기 쉽도록 별도 모듈로 둔다.
 * 실제 npm 패키지의 require()를 직접 목킹하는 것보다 로컬 상대 경로 모듈을 목킹하는 편이 테스트
 * 러너(vitest)에서 훨씬 안정적으로 동작한다.
 */
export function loadFastifyInstrumentation(): typeof import('@opentelemetry/instrumentation-fastify') {
  // eslint-disable-next-line @typescript-eslint/no-require-imports
  return require('@opentelemetry/instrumentation-fastify');
}
