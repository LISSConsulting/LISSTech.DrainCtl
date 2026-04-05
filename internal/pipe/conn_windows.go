//go:build windows

package pipe

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// pipeConn wraps a Windows named pipe handle as a net.Conn.
type pipeConn struct {
	file     *os.File
	handle   windows.Handle
	deadline time.Time
}

func newPipeConn(h windows.Handle, name string) *pipeConn {
	return &pipeConn{
		file:   os.NewFile(uintptr(h), name),
		handle: h,
	}
}

func (c *pipeConn) Read(b []byte) (int, error)  { return c.file.Read(b) }
func (c *pipeConn) Write(b []byte) (int, error) { return c.file.Write(b) }
func (c *pipeConn) Close() error                { return c.file.Close() }
func (c *pipeConn) LocalAddr() net.Addr         { return pipeAddr(PipeName) }
func (c *pipeConn) RemoteAddr() net.Addr        { return pipeAddr(PipeName) }

func (c *pipeConn) SetDeadline(t time.Time) error {
	c.deadline = t
	return c.file.SetDeadline(t)
}
func (c *pipeConn) SetReadDeadline(t time.Time) error  { return c.SetDeadline(t) }
func (c *pipeConn) SetWriteDeadline(t time.Time) error { return c.SetDeadline(t) }

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }

// acceptPipeConn creates a named pipe instance and waits for a client to
// connect. Supports cancellation via ctx.
func acceptPipeConn(ctx context.Context) (net.Conn, error) {
	pipePath, err := windows.UTF16PtrFromString(PipeName)
	if err != nil {
		return nil, err
	}

	h, err := windows.CreateNamedPipe(
		pipePath,
		windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED,
		windows.PIPE_TYPE_MESSAGE|windows.PIPE_READMODE_MESSAGE|windows.PIPE_WAIT,
		windows.PIPE_UNLIMITED_INSTANCES,
		65536,
		65536,
		0,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("CreateNamedPipe: %w", err)
	}

	// Use overlapped ConnectNamedPipe so we can cancel it.
	ol := &windows.Overlapped{}
	ol.HEvent, err = windows.CreateEvent(nil, 1, 0, nil) // manual reset
	if err != nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("CreateEvent: %w", err)
	}
	defer func() { _ = windows.CloseHandle(ol.HEvent) }()

	err = windows.ConnectNamedPipe(h, ol)
	if err == nil {
		// Client already connected before we called ConnectNamedPipe.
		return newPipeConn(h, PipeName), nil
	}
	if err != windows.ERROR_IO_PENDING {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("ConnectNamedPipe: %w", err)
	}

	// Create a cancel event from context.
	cancelEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("CreateEvent cancel: %w", err)
	}
	defer func() { _ = windows.CloseHandle(cancelEvent) }()

	go func() {
		<-ctx.Done()
		_ = windows.SetEvent(cancelEvent)
	}()

	handles := []windows.Handle{ol.HEvent, cancelEvent}
	idx, err := windows.WaitForMultipleObjects(handles, false, windows.INFINITE)
	if err != nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("WaitForMultipleObjects: %w", err)
	}

	if idx != windows.WAIT_OBJECT_0 {
		// Cancelled.
		_ = windows.CancelIo(h)
		_ = windows.CloseHandle(h)
		return nil, ctx.Err()
	}

	// Get the result of the overlapped ConnectNamedPipe.
	var bytesTransferred uint32
	err = windows.GetOverlappedResult(h, ol, &bytesTransferred, false)
	if err != nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("GetOverlappedResult: %w", err)
	}

	return newPipeConn(h, PipeName), nil
}

// dialPipe connects to the service's named pipe as a client.
func dialPipe() (net.Conn, error) {
	pipePath, err := windows.UTF16PtrFromString(PipeName)
	if err != nil {
		return nil, err
	}

	h, err := windows.CreateFile(
		pipePath,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		0,
		nil,
		windows.OPEN_EXISTING,
		0,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("connect to pipe: %w", err)
	}

	// Set message read mode.
	mode := uint32(windows.PIPE_READMODE_MESSAGE)
	err = windows.SetNamedPipeHandleState(h, &mode, nil, nil)
	if err != nil {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("set pipe mode: %w", err)
	}

	return newPipeConn(h, PipeName), nil
}

// windows.ERROR_IO_PENDING is needed for overlapped I/O.
func init() {
	// Ensure the constant is accessible (it's in x/sys/windows).
	_ = unsafe.Sizeof(windows.Overlapped{})
}
