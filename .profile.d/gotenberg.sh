export CHROMIUM_BIN_PATH="/app/chrome-headless-shell-linux64/chrome-headless-shell"
export CHROMIUM_HYPHEN_DATA_DIR_PATH="/app/chromium-hyphen-data"

export EXIFTOOL_BIN_PATH="/app/.apt/usr/bin/exiftool"
export PDFCPU_BIN_PATH="/app/bin/pdfcpu"
export QPDF_BIN_PATH="/app/.apt/usr/bin/qpdf"

export OTEL_LOGS_EXPORTER="none"
export OTEL_METRICS_EXPORTER="none"
# Without an exporter, Gotenberg logs an error after each conversion: sentry.incubateur.net does not accept OTLP traces
export OTEL_TRACES_EXPORTER="none"
