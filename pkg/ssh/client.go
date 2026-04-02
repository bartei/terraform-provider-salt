package ssh

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

// ConnectConfig holds SSH connection parameters including retry behavior.
type ConnectConfig struct {
	Host       string
	Port       int
	User       string
	PrivateKey string

	// Timeout is the SSH connection timeout (TCP + handshake).
	// Defaults to 30s if zero.
	Timeout time.Duration

	// MaxRetries is the number of connection attempts before giving up.
	// 0 means try once (no retries). Negative values are treated as 0.
	MaxRetries int

	// RetryInterval is the initial wait between retries. Each subsequent
	// retry doubles the interval (exponential backoff), capped at 30s.
	RetryInterval time.Duration
}

// Client wraps an SSH connection to a remote host.
type Client struct {
	client *ssh.Client
}

// NewClient establishes an SSH connection using a private key.
// For retry support, use NewClientWithRetry instead.
func NewClient(host string, port int, user string, privateKey string) (*Client, error) {
	return NewClientWithRetry(ConnectConfig{
		Host:       host,
		Port:       port,
		User:       user,
		PrivateKey: privateKey,
	})
}

// NewClientWithRetry establishes an SSH connection with configurable retries.
// On each failed attempt it waits with exponential backoff before retrying.
func NewClientWithRetry(cfg ConnectConfig) (*Client, error) {
	signer, err := ssh.ParsePrivateKey([]byte(cfg.PrivateKey))
	if err != nil {
		return nil, fmt.Errorf("parsing private key: %w", err)
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	sshConfig := &ssh.ClientConfig{
		User: cfg.User,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         timeout,
	}

	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))

	maxRetries := cfg.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}

	retryInterval := cfg.RetryInterval
	if retryInterval <= 0 {
		retryInterval = 5 * time.Second
	}

	const maxBackoff = 30 * time.Second

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(retryInterval)
			// Exponential backoff, capped
			retryInterval *= 2
			if retryInterval > maxBackoff {
				retryInterval = maxBackoff
			}
		}

		client, err := ssh.Dial("tcp", addr, sshConfig)
		if err == nil {
			return &Client{client: client}, nil
		}
		lastErr = err
	}

	if maxRetries > 0 {
		return nil, fmt.Errorf("connecting to %s: %w (gave up after %d retries)", addr, lastErr, maxRetries)
	}
	return nil, fmt.Errorf("connecting to %s: %w", addr, lastErr)
}

// RunResult holds the output of a remote command execution.
type RunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Run executes a command on the remote host and returns stdout.
// Returns an error if the command exits with a non-zero status.
// Use RunCapture when you need separate access to stderr and exit code.
func (c *Client) Run(cmd string) (string, error) {
	r, err := c.RunCapture(cmd)
	if err != nil {
		return r.Stdout, err
	}
	if r.ExitCode != 0 {
		return r.Stdout, fmt.Errorf("command failed: %w\nstderr: %s",
			&ExitError{Code: r.ExitCode}, r.Stderr)
	}
	return r.Stdout, nil
}

// ExitError represents a non-zero exit code from a remote command.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("Process exited with status %d", e.Code)
}

// RunCapture executes a command and returns stdout, stderr, and exit code
// separately. Returns a non-nil error only for SSH-level failures (session
// creation, network errors). Command failures (non-zero exit) are reported
// via RunResult.ExitCode — the caller decides if that's an error.
func (c *Client) RunCapture(cmd string) (RunResult, error) {
	session, err := c.client.NewSession()
	if err != nil {
		return RunResult{}, fmt.Errorf("creating session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	exitCode := 0
	if err := session.Run(cmd); err != nil {
		if exitErr, ok := err.(*ssh.ExitError); ok {
			exitCode = exitErr.ExitStatus()
		} else {
			return RunResult{Stdout: stdout.String(), Stderr: stderr.String()},
				fmt.Errorf("command failed: %w\nstderr: %s", err, stderr.String())
		}
	}

	return RunResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}, nil
}

// Upload writes content to a remote file path via cat.
func (c *Client) Upload(remotePath string, content []byte) error {
	session, err := c.client.NewSession()
	if err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	defer session.Close()

	// Create parent directory
	dir := remotePath[:len(remotePath)-len(remotePath[lastSlash(remotePath):])]
	if dir != "" {
		if _, err := c.Run(fmt.Sprintf("mkdir -p %s", dir)); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}

		// Need a new session after Run
		session.Close()
		session, err = c.client.NewSession()
		if err != nil {
			return fmt.Errorf("creating session: %w", err)
		}
	}

	session.Stdin = io.NopCloser(bytes.NewReader(content))

	var stderr bytes.Buffer
	session.Stderr = &stderr

	if err := session.Run(fmt.Sprintf("cat > %s", remotePath)); err != nil {
		return fmt.Errorf("uploading to %s: %w\nstderr: %s", remotePath, err, stderr.String())
	}

	return nil
}

// Close closes the SSH connection.
func (c *Client) Close() error {
	return c.client.Close()
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return 0
}
