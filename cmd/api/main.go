package main

import (
	"context"
	"database/sql"
	"expvar"
	"flag"
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Soul-Remix/greenlight/internal/data"
	"github.com/Soul-Remix/greenlight/internal/jsonlog"
	"github.com/Soul-Remix/greenlight/internal/mailer"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

const version = "1.0.0"

type config struct {
	port string
	env  string
	db   struct {
		dsn          string
		maxOpenConns string
		maxIdleConns string
		maxIdleTime  string
	}
	limiter struct {
		rps     int
		burst   int
		enabled bool
	}
	smtp struct {
		host     string
		port     int
		username string
		password string
		sender   string
	}
	cors struct {
		trustedOrigins []string
	}
}

type application struct {
	config config
	logger *jsonlog.Logger
	models data.Models
	mailer mailer.Mailer
	wg     sync.WaitGroup
}

func init() {
	err := godotenv.Load(".env")

	if err != nil {
		log.Fatal("Error loading .env file")
	}
}

func main() {
	var cfg config

	flag.StringVar(&cfg.port, "port", getEnv("PORT", "4000"), "API server port")

	flag.StringVar(&cfg.env, "env", getEnv("ENVIRONMENT", "development"), "Environment (development|staging|production)")
	flag.StringVar(&cfg.db.dsn, "db-dsn", os.Getenv("GREENLIGHT_DB_DSN"), "PostgreSQL DSN")
	flag.StringVar(&cfg.db.maxOpenConns, "db-max-open-conns", getEnv("DB_MAX_IDLE_TIME", "25"), "PostgreSQL max open connections")
	flag.StringVar(&cfg.db.maxIdleConns, "db-max-idle-conns", getEnv("DB_MAX_IDLE_TIME", "25"), "PostgreSQL max idle connections")
	flag.StringVar(&cfg.db.maxIdleTime, "db-max-idle-time", getEnv("DB_MAX_IDLE_TIME", "15m"), "PostgreSQL max connection idle time")

	flag.IntVar(&cfg.limiter.rps, "limiter-rps", getIntEnv("LIMITER_RPS", 2), "Rate limiter maximum requests per second")
	flag.IntVar(&cfg.limiter.burst, "limiter-burst", getIntEnv("LIMITER_BURST", 4), "Rate limiter maximum burst")
	flag.BoolVar(&cfg.limiter.enabled, "limiter-enabled", getBoolEnv("LIMITER_ENABLED", true), "Enable rate limiter")

	flag.StringVar(&cfg.smtp.host, "smtp-host", getEnv("SMTP_HOST", ""), "SMTP host")
	flag.IntVar(&cfg.smtp.port, "smtp-port", getIntEnv("SMTP_PORT", 25), "SMTP port")
	flag.StringVar(&cfg.smtp.username, "smtp-username", getEnv("SMTP_USERNAME", ""), "SMTP username")
	flag.StringVar(&cfg.smtp.password, "smtp-password", getEnv("SMTP_PASSWORD", ""), "SMTP password")
	flag.StringVar(&cfg.smtp.sender, "smtp-sender", getEnv("SMTP_SENDER", "15m"), "SMTP sender")

	flag.Func("cors-trusted-origins", "Trusted CORS origins", func(val string) error {
		cfg.cors.trustedOrigins = strings.Split(getEnv("CORS_TRUSTED_ORIGIN", "*"), ",")
		return nil
	})

	flag.Parse()

	logger := jsonlog.New(os.Stdout, jsonlog.LevelInfo)

	db, err := openDB(cfg)
	if err != nil {
		logger.PrintFatal(err, nil)
	}

	defer db.Close()
	logger.PrintInfo("database connection pool established", nil)

	expvar.NewString("version").Set(version)

	expvar.Publish("goroutines", expvar.Func(func() any {
		return runtime.NumGoroutine()
	}))

	expvar.Publish("database", expvar.Func(func() any {
		return db.Stats()
	}))

	expvar.Publish("timestamp", expvar.Func(func() any {
		return time.Now().Unix()
	}))

	app := &application{
		config: cfg,
		logger: logger,
		models: data.NewModels(db),
		mailer: mailer.New(cfg.smtp.host, cfg.smtp.port, cfg.smtp.username, cfg.smtp.password, cfg.smtp.sender),
		wg:     sync.WaitGroup{},
	}

	err = app.serve()
	if err != nil {
		logger.PrintFatal(err, nil)
	}
}

func getEnv(env string, value string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return value
}

func getIntEnv(env string, value int) int {
	v := os.Getenv(env)
	if v == "" {
		return value
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Fatal("failed to parse int env variable")
	}
	return n
}

func getBoolEnv(env string, value bool) bool {
	v := os.Getenv(env)
	if v == "" {
		return value
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		log.Fatal("failed to parse bool env variable")
	}
	return b
}

func openDB(cfg config) (*sql.DB, error) {
	db, err := sql.Open("postgres", cfg.db.dsn)
	if err != nil {
		return nil, err
	}

	maxOpenCon, _ := strconv.Atoi(cfg.db.maxOpenConns)
	maxIdleCon, _ := strconv.Atoi(cfg.db.maxIdleConns)

	db.SetMaxOpenConns(maxOpenCon)
	db.SetMaxIdleConns(maxIdleCon)

	duration, err := time.ParseDuration(cfg.db.maxIdleTime)
	if err != nil {
		return nil, err
	}

	db.SetConnMaxIdleTime(duration)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = db.PingContext(ctx)
	if err != nil {
		return nil, err
	}

	return db, nil
}
