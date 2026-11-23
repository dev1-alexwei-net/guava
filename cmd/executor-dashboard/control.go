package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/dev1-alexwei-net/guava/executor"
)

const maxLogBytes = 64 << 10

type executorController struct {
	mu      sync.Mutex
	runner  *executor.Executor
	logger  *slog.Logger
	logs    *tailLogBuffer
	status  string
	lastErr string
	cancel  context.CancelFunc
	done    chan struct{}
}

type executorSnapshot struct {
	Status  string
	Logs    string
	LastErr string
}

type tailLogBuffer struct {
	mu      sync.Mutex
	content []byte
}

func (b *tailLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.content = append(b.content, p...)
	if len(b.content) > maxLogBytes {
		b.content = append([]byte(nil), b.content[len(b.content)-maxLogBytes:]...)
	}
	return len(p), nil
}

func (b *tailLogBuffer) Reset() {
	b.mu.Lock()
	b.content = nil
	b.mu.Unlock()
}

func (b *tailLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.content)
}

func newExecutorController(config executor.Config, level slog.Level) (*executorController, error) {
	logs := &tailLogBuffer{}
	logger := executor.NewTextLogger(logs, level)
	config.Logger = logger

	runner, err := executor.New(config)
	if err != nil {
		return nil, err
	}
	return &executorController{
		runner: runner,
		logger: logger,
		logs:   logs,
		status: "idle",
	}, nil
}

func (c *executorController) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.status == "running" || c.status == "stopping" {
		return errors.New("executor is already running")
	}
	c.logs.Reset()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.cancel = cancel
	c.done = done
	c.status = "running"
	c.lastErr = ""
	c.logger.Info("Executor start requested")
	go c.run(ctx, done)
	return nil
}

func (c *executorController) run(ctx context.Context, done chan struct{}) {
	err := c.runner.Run(ctx)

	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	c.done = nil
	switch {
	case errors.Is(err, context.Canceled):
		c.status = "stopped"
	case err != nil:
		c.status = "failed"
		c.lastErr = err.Error()
	default:
		c.status = "completed"
	}
	close(done)
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

func (c *executorController) Stop() error {
	c.mu.Lock()
	if c.status != "running" {
		c.mu.Unlock()
		return errors.New("executor is not running")
	}
	cancel := c.cancel
	c.status = "stopping"
	c.mu.Unlock()

	c.logger.Info("Executor stop requested")
	if cancel != nil {
		cancel()
	}
	return nil
}

func (c *executorController) Snapshot() executorSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return executorSnapshot{
		Status:  c.status,
		Logs:    c.logs.String(),
		LastErr: c.lastErr,
	}
}

func (c *executorController) Close() {
	c.mu.Lock()
	cancel := c.cancel
	done := c.done
	if c.status == "running" {
		c.status = "stopping"
	}
	c.mu.Unlock()

	if cancel != nil {
		c.logger.Info("Executor stop requested by dashboard shutdown")
		cancel()
	}
	if done != nil {
		<-done
	}
}
