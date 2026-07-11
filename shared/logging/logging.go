// Package logging configures logrus (app.log) and gin (gin.log) to write
// date-partitioned, size-rotated files under LOG_DIR/<serviceName>.
package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	"gopkg.in/natefinch/lumberjack.v2"
)

const logMaxSizeMB = 15

// dailyLumberjackWriter writes to a date-based path (one directory per day) with
// size rotation. When the date changes it closes the current file and opens a
// new one under the new day's directory.
type dailyLumberjackWriter struct {
	baseDir  string
	baseName string
	current  *lumberjack.Logger
	date     string
	mu       sync.Mutex
}

func (w *dailyLumberjackWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	today := time.Now().Format("2006-01-02")
	if w.date != today || w.current == nil {
		if w.current != nil {
			_ = w.current.Close()
		}
		w.date = today

		dayDir := filepath.Join(w.baseDir, today)
		if err := os.MkdirAll(dayDir, os.ModePerm); err != nil {
			return 0, err
		}
		w.current = &lumberjack.Logger{
			Filename: filepath.Join(dayDir, w.baseName),
			MaxSize:  logMaxSizeMB,
			Compress: false,
		}
	}
	return w.current.Write(p)
}

// rotateAtStartup renames today's current log file to a "-restarted-<ts>" backup
// so a restart does not append to / overwrite the pre-restart file. No-op when
// the file does not exist.
func rotateAtStartup(baseDir, fileName string) error {
	today := time.Now().Format("2006-01-02")
	dayDir := filepath.Join(baseDir, today)
	currentLogPath := filepath.Join(dayDir, fileName)

	if _, statErr := os.Stat(currentLogPath); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil
		}
		return statErr
	}

	ext := filepath.Ext(fileName)
	base := strings.TrimSuffix(fileName, ext)
	timestamp := fmt.Sprintf("restarted-%s", time.Now().Format("2006-01-02T15-04-05.000"))
	backupName := fmt.Sprintf("%s-%s%s", base, timestamp, ext)
	backupPath := filepath.Join(dayDir, backupName)

	return os.Rename(currentLogPath, backupPath)
}

// Setup wires logrus (app.log, file-only) and gin (gin.log + stdout/stderr) to
// date-partitioned, size-rotated files under LOG_DIR/<serviceName>. LOG_DIR
// defaults to /data/logs; LOG_LEVEL defaults to info. On any setup error it logs
// and returns so the service keeps running (logging to the logrus default).
func Setup(serviceName string) {
	log.SetReportCaller(true)
	log.SetFormatter(&customFormatter{})

	level, err := log.ParseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		level = log.InfoLevel
	}
	log.SetLevel(level)

	baseDir := os.Getenv("LOG_DIR")
	if baseDir == "" {
		baseDir = "/data/logs"
	}
	logDir := filepath.Join(baseDir, serviceName)

	if err := os.MkdirAll(logDir, os.ModePerm); err != nil {
		log.Errorln("logging.Setup: cannot create log dir:", err)
		return
	}

	if err := rotateAtStartup(logDir, "app.log"); err != nil {
		log.Errorln("logging.Setup: cannot rotate app.log at startup:", err)
		return
	}
	if err := rotateAtStartup(logDir, "gin.log"); err != nil {
		log.Errorln("logging.Setup: cannot rotate gin.log at startup:", err)
		return
	}

	appWriter := &dailyLumberjackWriter{baseDir: logDir, baseName: "app.log"}
	ginWriter := &dailyLumberjackWriter{baseDir: logDir, baseName: "gin.log"}

	gin.DefaultWriter = io.MultiWriter(ginWriter, os.Stdout)
	gin.DefaultErrorWriter = io.MultiWriter(ginWriter, os.Stderr)

	log.SetOutput(appWriter)
}

type customFormatter struct{}

func (f *customFormatter) Format(entry *log.Entry) ([]byte, error) {
	var fn, file string
	var line int
	if entry.Caller != nil {
		fn = entry.Caller.Function
		file = entry.Caller.File
		line = entry.Caller.Line
	}
	return []byte(fmt.Sprintf("time=%q func=%s file=%s line=%d level=%s msg=%q \n",
		entry.Time.Format("2006/01/02 15:04:05.000000000"),
		fn, file, line,
		entry.Level.String(),
		entry.Message,
	)), nil
}
