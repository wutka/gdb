package gdb

import (
	"github.com/kr/pty"
	"io"
	"os"
	"os/exec"
	"sync"
)

// Gdb represents a GDB instance. It implements the ReadWriter interface to
// read/write data from/to the targer program's TTY.
type Gdb struct {
	io.ReadWriter

	ptm *os.File
	pts *os.File
	cmd *exec.Cmd

	mutex  sync.RWMutex
	stdin  io.WriteCloser
	stdout io.ReadCloser

	sequence int64
	pending  map[string]chan map[string]interface{}

	onNotification NotificationCallback
}

// New creates and start a new GDB instance. onNotification if not nil is the
// callback used deliver to the client the asynchronous notifications sent by
// GDB. It returns a pointer to the newly created instance handled or an error.
func New(onNotification NotificationCallback) (*Gdb, error) {
	gdb := Gdb{onNotification: onNotification}

	// open a new terminal (master and slave) for the target program, they are
	// both saved so that they are nore garbage collected after this function
	// ends
	ptm, pts, err := pty.Open()
	if err != nil {
		return nil, err
	}
	gdb.ptm = ptm
	gdb.pts = pts

	// create GDB command
	gdb.cmd = exec.Command("gdb", "--nx", "--quiet", "--interpreter=mi2", "--tty", pts.Name())

	// GDB standard input
	stdin, err := gdb.cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	gdb.stdin = stdin

	// GDB standard ouput
	stdout, err := gdb.cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	gdb.stdout = stdout

	// start GDB
	if err := gdb.cmd.Start(); err != nil {
		return nil, err
	}

	// prepare the command interface
	gdb.sequence = 1
	gdb.pending = make(map[string]chan map[string]interface{})

	// start the line reader
	go gdb.recordReader()

	return &gdb, nil
}

// Read reads a number of bytes from the target program's output.
func (gdb *Gdb) Read(p []byte) (n int, err error) {
	return gdb.ptm.Read(p)
}

// Write writes a number of bytes to the target program's input.
func (gdb *Gdb) Write(p []byte) (n int, err error) {
	return gdb.ptm.Write(p)
}

// Interrupt sends a signal (SIGINT) to GDB so it can stop the target program
// and resume the processing of commands.
func (gdb *Gdb) Interrupt() error {
	return gdb.cmd.Process.Signal(os.Interrupt)
}

// Exit sends the exit command to GDB and waits for the process to exit.
func (gdb *Gdb) Exit() error {
	// send the exit command and wait for the GDB process
	if _, err := gdb.Send("gdb-exit"); err != nil {
		return err
	}
	if err := gdb.cmd.Wait(); err != nil {
		return err
	}

	// close the target program's terminal, since the lifetime of the terminal
	// is longer that the one of the targer program's instances reading from a
	// Gdb object ,i.e., the master side will never return EOF (at least on
	// Linux) so the only way to stop reading is to intercept the I/O error
	// caused by closing the terminal
	if err := gdb.ptm.Close(); err != nil {
		return err
	}
	if err := gdb.pts.Close(); err != nil {
		return err
	}

	return nil
}
