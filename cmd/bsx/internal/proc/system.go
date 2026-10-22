package proc

import (
	"context"
	"syscall"
)

// System is the machine's process table, as the core's Proc port sees it.
type System struct{}

func (System) Alive(p Process) (bool, error)              { return Alive(p.PID, p.Start) }
func (System) Start(pid int) (string, error)              { return Start(pid) }
func (System) Parent(pid int) (int, error)                { return Parent(pid) }
func (System) Group(pid int) (int, error)                 { return Group(pid) }
func (System) Descendants(pid int) ([]Process, error)     { return Descendants(pid) }
func (System) Command(pid int) (string, error)            { return Command(pid) }
func (System) Exe(pid int) (string, error)                { return Exe(pid) }
func (System) Signal(p Process, sig syscall.Signal) error { return Signal(p, sig) }
func (System) Listening(ctx context.Context, pids []int) (map[int][]int, error) {
	return Listening(ctx, pids)
}
