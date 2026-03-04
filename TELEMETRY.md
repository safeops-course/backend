# Backend Telemetry

Този документ описва текущото състояние на телеметрията в backend услугата.

## Обхват

Backend праща:
- traces (OpenTelemetry + Uptrace exporter)
- логове с корелация към trace/span
- Prometheus метрики през `/metrics`
- OpenTelemetry метрики (OTel meter инструменти)

## Инициализация

Основен flow:
- `cmd/api/main.go` извиква `telemetry.Init(ctx)` при startup.
- Ако `UPTRACE_DSN` е зададен, се конфигурира Uptrace exporter.
- Ако `UPTRACE_DSN` липсва, услугата работи локално без remote export.
- При shutdown се извиква `uptrace.Shutdown(ctx)`.

Ресурсни атрибути:
- `service.name` (по подразбиране `backend`)
- `service.version` (по подразбиране `v1.0.0`)
- `deployment.environment.name` (по подразбиране `development`)

## Tracing

HTTP tracing:
- `pkg/telemetry/middleware.go` използва `otelhttp.NewHandler`.
- Име на span: `METHOD /path` (пример: `GET /version`).
- Филтър за шум: не се trace-ват `/healthz`, `/readyz`, `/livez`.

Propagation:
- Глобално е включен W3C propagation:
- `traceparent`
- `tracestate`
- `baggage`

Span helpers:
- `telemetry.StartSpan`
- `telemetry.AddEvent`
- `telemetry.SetAttributes`
- `telemetry.RecordError` (маркира span със status=ERROR)

Примери в кода:
- Request log event: `request.log` в `loggingMiddleware`.
- Chaos panic event: `panic.log` в `/panic`.

## Логове и корелация

Логър:
- `pkg/logger/logger.go` използва `otelzap` върху `zap`.

Trace корелация в runtime логове:
- `pkg/server/log_context.go` добавя `trace_id` и `span_id` от request context.
- Това се ползва в:
- request логовете (`loggingMiddleware`)
- auth/token handlers
- `/error/*` и `/panic` handlers

Важно:
- startup/fatal логове извън request context естествено може да нямат `trace_id/span_id`.

## Метрики

### Prometheus endpoint

- Endpoint: `GET /metrics`
- Регистър: custom `prometheus.Registry` в `Server`
- Метрики:
- `app_http_requests_total{method,path,status}`
- `app_http_request_duration_seconds{method,path}`
- `app_http_in_flight_requests`
- Go/process collectors

### OpenTelemetry meter инструменти

Създават се в `pkg/telemetry/telemetry.go`:
- `backend.requests.total` (counter)
- `backend.errors.total` (counter)
- `backend.request.duration` (histogram)

`telemetry.RecordRequest(...)` инкрементира тези метрики, когато бъде извикан.

## CORS и trace headers

В `corsMiddleware` са позволени:
- `traceparent`
- `tracestate`
- `baggage`

Така frontend може да пропагира trace контекст към backend.

## Конфигурация

Ключови env променливи:
- `UPTRACE_DSN`
- `SERVICE_NAME`
- `SERVICE_VERSION`
- `DEPLOYMENT_ENVIRONMENT`

## Бърз checklist за валидация

1. Backend стартира и логва `OpenTelemetry initialized` или съобщение за липсващ `UPTRACE_DSN`.
2. Заявка към endpoint като `/version` се вижда в Uptrace като HTTP span.
3. Request логът за същата заявка съдържа `trace_id` и `span_id`.
4. `/metrics` връща `app_http_requests_total` и `app_http_request_duration_seconds`.
