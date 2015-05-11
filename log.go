package logging

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/DramaFever/raven-go"
)

const (
	// DebugLvl indicates messages for local development
	DebugLvl Level = "DEBUG"
	// InfoLvl indicates non-error messages useful to Ops
	InfoLvl Level = "INFO"
	// WarnLvl indicates recoverable error messages
	WarnLvl Level = "WARN"
	// ErrorLvl indicates non-recoverable error messages
	ErrorLvl Level = "ERROR"
)

// Level is a threshold used to constrain which logs are written in which environments.
// When set to DebugLvl, all output is written.
// When set to InfoLvl, all output except DebugLvl statements are written.
// When set to WarnLvl, all output except DebugLvl and InfoLvl statements are written.
// When set to ErrorLvl, all output except DebugLvl, InfoLvl, and WarnLvl statements are written.
// When set to any other value, all output is written.
//
// Choosing which Level to use is a bit of an art. As a guideline: anything that Ops cannot use to
// investigate an issue in production should be at DebugLvl. ErrorLvl is for fatal errors that result
// in a 500. WarnLvl is for recoverable errors that you can gracefully degrade from, but which are not
// expected occurrences.
//
// An example of an ErrorLvl scenario may be that your binary can't reach the database.
// An example of a WarnLvl scenario may be that you temporarily couldn't reach an external service, but
// are retrying. (If your retrying logic fails, escalate it to an ErrorLvl.)
// An example of an InfoLvl scenario may be the port and address a host is listening on.
// An example of a DebugLvl scenario may be an incoming request, a cache miss, the response from an
// external service, the database query ran, etc. Things that are useful when writing software, but
// too noisy to have on in production.
type Level string

// includes returns true if l "includes" other. l includes other when a message logged at other's Level
// should be included in a log file that requires at least l severity.
func (l Level) includes(other Level) bool {
	switch l {
	case InfoLvl:
		return other != DebugLvl
	case WarnLvl:
		return other != DebugLvl && other != InfoLvl
	case ErrorLvl:
		return other != DebugLvl && other != InfoLvl && other != WarnLvl
	default:
		return true
	}
}

// Logger is an instance of a log handler, used to write files to the designated output
// if they meet the specified Level. It is concurrency-safe. Each Logger should have its
// Close method called when you're done with it.
type Logger struct {
	level     Level
	out       io.Writer
	sentry    *raven.Client
	calldepth int
	buf       []byte
	lock      *sync.Mutex
}

// LogToFile creates a new Logger that writes to a file specified by path. If the file doesn't exist, it
// will be created. If it does exist, new log lines will be appended to it.
//
// If sentry is non-empty, it will be used as a DSN to connect to a Sentry error collector. The sentryTags
// are a key/value mapping that will be applied to your Sentry errors. You can use them to set things like
// the version of your software running, etc.
func LogToFile(level Level, path string, sentry string, sentryTags map[string]string) (Logger, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return Logger{}, err
	}
	return New(level, f, sentry, sentryTags)
}

// LogToStdout creates a new Logger that writes to stdout.
//
// If sentry is non-empty, it will be used as a DSN to connect to a Sentry error collector. The sentryTags
// are a key/value mapping that will be applied to your Sentry errors. You can use them to set things like
// the version of your software running, etc.
func LogToStdout(level Level, sentry string, sentryTags map[string]string) (Logger, error) {
	return New(level, os.Stdout, sentry, sentryTags)
}

// New creates a new Logger that writes to the io.Writer specified. If the io.Writer is an io.WriteCloser,
// it will be automatically closed when the Logger's Close method is called.
//
// If sentry is non-empty, it will be used as a DSN to connect to a Sentry error collector. The sentryTags
// are a key/value mapping that will be applied to your Sentry errors. You can use them to set things like
// the version of your software running, etc.
func New(level Level, out io.Writer, sentry string, sentryTags map[string]string) (Logger, error) {
	var sentryClient *raven.Client
	var err error
	if sentry != "" {
		sentryClient, err = raven.NewClient(sentry, sentryTags)
	}
	return Logger{
		level:  level,
		out:    out,
		sentry: sentryClient,
		lock:   new(sync.Mutex),
	}, err
}

// Close signifies that a Logger will no longer be used, and the resources allocated to it can be freed.
// Once the Close method is called, you should not write any more logs using that Logger. Create a new one
// instead.
func (l Logger) Close() {
	l.sentry.Close()
	if closer, ok := l.out.(io.Closer); ok {
		closer.Close()
	}
}

// GetLevel returns the Level assigned to the Logger.
func (l Logger) GetLevel() Level {
	l.lock.Lock()
	defer l.lock.Unlock()
	return l.level
}

// SetLevel updates the Level assigned to the Logger.
func (l *Logger) SetLevel(lvl Level) {
	l.lock.Lock()
	defer l.lock.Unlock()
	l.level = lvl
}

// SetOutput redirects the logs from the Logger to a new destination.
func (l *Logger) SetOutput(out io.Writer) {
	l.lock.Lock()
	defer l.lock.Unlock()
	l.out = out
}

// SetCallDepth is useful for helper libraries that wrap this, and call their helpers. The call depth is
// how many calls up the stack the Logger should look when deciding what file/line combo created the log
// statement. This defaults to 0, which is accurate if you're just calling the Logger directly. For every
// level of indirection, add 1.
func (l *Logger) SetCallDepth(depth int) {
	l.lock.Lock()
	defer l.lock.Unlock()
	l.calldepth = depth
}

// SetSentry updates the DSN and tags that will be used to send errors to Sentry.
func (l *Logger) SetSentry(dsn string, tags map[string]string) error {
	l.lock.Lock()
	defer l.lock.Unlock()
	sentryClient, err := raven.NewClient(dsn, tags)
	if err != nil {
		return err
	}
	if l.sentry != nil {
		l.sentry.Close()
	}
	l.sentry = sentryClient
	return nil
}

// Debugf writes a log entry with the Level of DebugLvl, interpolating the format
// string with the arguments passed. See fmt.Sprintf for information on variable
// placeholders in the format string.
func (l Logger) Debugf(format string, msg ...interface{}) {
	l.lock.Lock()
	defer l.lock.Unlock()
	if l.out == nil {
		return
	}
	if !l.level.includes(DebugLvl) {
		return
	}
	l.logf(format, msg...)
}

// Debug writes a log entry with the Level of DebugLvl, joining each argument passed
// with a space.
func (l Logger) Debug(msg ...interface{}) {
	l.lock.Lock()
	defer l.lock.Unlock()
	if l.out == nil {
		return
	}
	if !l.level.includes(DebugLvl) {
		return
	}
	l.log(msg...)
}

// Infof writes a log entry with the Level of InfoLvl, interpolating the format
// string with the arguments passed. See fmt.Sprintf for information on variable
// placeholders in the format string.
func (l Logger) Infof(format string, msg ...interface{}) {
	l.lock.Lock()
	defer l.lock.Unlock()
	if l.out == nil {
		return
	}
	if !l.level.includes(InfoLvl) {
		return
	}
	l.logf(format, msg...)
}

// Info writes a log entry with the Level of InfoLvl, joining each argument passed
// with a space.
func (l Logger) Info(msg ...interface{}) {
	l.lock.Lock()
	defer l.lock.Unlock()
	if l.out == nil {
		return
	}
	if !l.level.includes(InfoLvl) {
		return
	}
	l.log(msg...)
}

// Warnf writes a log entry with the Level of WarnLvl, interpolating the format
// string with the arguments passed. See fmt.Sprintf for information on variable
// placeholders in the format string.
//
// Any message logged with Warnf will automatically be sent to Sentry, if Sentry
// has been configured.
func (l Logger) Warnf(format string, msg ...interface{}) {
	l.lock.Lock()
	defer l.lock.Unlock()
	if l.out == nil {
		return
	}
	if !l.level.includes(WarnLvl) {
		return
	}
	l.logf(format, msg...)
}

// Warn writes a log entry with the Level of WarnLvl, joining each argument passed
// with a space.
//
// Any message logged with Warn will automatically be sent to Sentry, if Sentry
// has been configured.
func (l Logger) Warn(msg ...interface{}) {
	l.lock.Lock()
	defer l.lock.Unlock()
	if l.out == nil {
		return
	}
	if !l.level.includes(WarnLvl) {
		return
	}
	l.log(msg...)
}

// Errorf writes a log entry with the Level of ErrorLvl, interpolating the format
// string with the arguments passed. See fmt.Sprintf for information on variable
// placeholders in the format string.
//
// Any message logged with Errorf will automatically be sent to Sentry, if Sentry
// has been configured.
func (l Logger) Errorf(format string, msg ...interface{}) {
	l.lock.Lock()
	defer l.lock.Unlock()
	if l.out == nil {
		return
	}
	if !l.level.includes(ErrorLvl) {
		return
	}
	l.logf(format, msg...)
}

// Error writes a log entry with the Level of ErrorLvl, joining each argument passed
// with a space.
//
// Any message logged with Error will automatically be sent to Sentry, if Sentry
// has been configured.
func (l Logger) Error(msg ...interface{}) {
	l.lock.Lock()
	defer l.lock.Unlock()
	if l.out == nil {
		return
	}
	if !l.level.includes(ErrorLvl) {
		return
	}
	l.log(msg...)
}

func (l Logger) log(msg ...interface{}) {
	err := l.output(l.calldepth+2, fmt.Sprint(msg...))
	if err != nil {
		os.Stderr.Write([]byte(time.Now().String() + " " + err.Error()))
	}
}

func (l Logger) logf(format string, msg ...interface{}) {
	err := l.output(l.calldepth+2, fmt.Sprintf(format, msg...))
	if err != nil {
		os.Stderr.Write([]byte(time.Now().String() + " " + err.Error()))
	}
}

// Cheap integer to fixed-width decimal ASCII.  Give a negative width to avoid zero-padding.
// Knows the buffer has capacity.
//
// stolen shamelessly from https://github.com/golang/go/blob/883bc6ed0ea815293fe6309d66f967ea60630e87/src/log/log.go#L60
func itoa(buf *[]byte, i int, wid int) {
	var u = uint(i)
	if u == 0 && wid <= 1 {
		*buf = append(*buf, '0')
		return
	}

	// Assemble decimal in reverse order.
	var b [32]byte
	bp := len(b)
	for ; u > 0 || wid > 0; u /= 10 {
		bp--
		wid--
		b[bp] = byte(u%10) + '0'
	}
	*buf = append(*buf, b[bp:]...)
}

// Prepend our log header to the buffer.
//
// Heavily modified form of https://github.com/golang/go/blob/883bc6ed0ea815293fe6309d66f967ea60630e87/src/log/log.go#L80
func (l *Logger) formatHeader(buf *[]byte, now time.Time, file string, line int, level Level) {
	year, month, day := now.Date()
	itoa(buf, year, 4)
	*buf = append(*buf, '-')
	itoa(buf, int(month), 2)
	*buf = append(*buf, '-')
	itoa(buf, day, 2)
	*buf = append(*buf, 'T')
	hour, minute, second := now.Clock()
	itoa(buf, hour, 2)
	*buf = append(*buf, ':')
	itoa(buf, minute, 2)
	*buf = append(*buf, ':')
	itoa(buf, second, 2)

	*buf = append(*buf, " ["+string(level)+"] "...)

	*buf = append(*buf, file...)
	*buf = append(*buf, ':')
	itoa(buf, line, -1)
	*buf = append(*buf, ": "...)
}

// Actually write to l.out after gathering caller information
//
// Heavily modified version of https://github.com/golang/go/blob/883bc6ed0ea815293fe6309d66f967ea60630e87/src/log/log.go#L130
func (l *Logger) output(calldepth int, s string) error {
	now := time.Now()
	l.lock.Unlock() // release lock while grabbing caller info - it's expensive
	_, file, line, ok := runtime.Caller(calldepth)
	if !ok {
		file = "???"
		line = 0
	}
	l.lock.Lock()
	l.buf = l.buf[:0]
	l.formatHeader(&l.buf, now, file, line, l.level)
	l.buf = append(l.buf, s...)
	if len(s) > 0 && s[len(s)-1] != '\n' {
		l.buf = append(l.buf, '\n')
	}
	_, err := l.out.Write(l.buf)
	return err
}
