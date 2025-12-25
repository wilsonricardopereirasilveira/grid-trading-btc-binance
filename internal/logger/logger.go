package logger

import (
	"log/slog"
	"os"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

var Log *slog.Logger

func Init() {
	// Ensure logs directory exists
	// We're running from root usually, but let's be safe.
	// "logs" dir validation.
	// Note: We need to import "os"
	_ = os.MkdirAll("logs", 0755)

	fileWriter := &lumberjack.Logger{
		Filename:   "logs/app.log",
		MaxSize:    10, // megabytes
		MaxBackups: 3,
		MaxAge:     28,   // days
		Compress:   true, // disabled by default
	}

	// Configurar Timezone Global para SP
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		// Fallback se não tiver tzdata (comum em docker alpine, mas vps linux geralmente tem)
		// Vamos tentar hardcoded offset -3 se falhar
		time.Local = time.FixedZone("BRT", -3*60*60)
	} else {
		time.Local = loc
	}

	opts := &slog.HandlerOptions{
		Level: slog.LevelDebug,
		// Custom Replacement for Time format if needed, but changing Global Time.Local usually affects slog default handler
	}

	// Use JSON handler for structured logging
	handler := slog.NewJSONHandler(fileWriter, opts)
	Log = slog.New(handler)
	slog.SetDefault(Log)
}

func Info(msg string, args ...any) {
	if Log != nil {
		Log.Info(msg, args...)
	}
}

func Error(msg string, args ...any) {
	if Log != nil {
		Log.Error(msg, args...)
	}
}

func Warn(msg string, args ...any) {
	if Log != nil {
		Log.Warn(msg, args...)
	}
}

func Debug(msg string, args ...any) {
	if Log != nil {
		Log.Debug(msg, args...)
	}
}
