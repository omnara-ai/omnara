package tools

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	seccomp "github.com/elastic/go-seccomp-bpf"
	"github.com/elastic/go-seccomp-bpf/arch"
	"github.com/landlock-lsm/go-landlock/landlock"
	ll "github.com/landlock-lsm/go-landlock/landlock/syscall"
	"golang.org/x/sys/unix"
)

func RunSearchProcess(args []string) error {
	if len(args) < 2 || !filepath.IsAbs(args[0]) {
		return errors.New("invalid search process arguments")
	}
	root := os.NewFile(3, "memory store")
	if err := root.Chdir(); err != nil {
		_ = root.Close()
		return err
	}
	if err := root.Close(); err != nil {
		return err
	}
	info, err := arch.GetInfo("")
	if err != nil {
		return err
	}
	var names []string
	for _, name := range strings.Fields(
		"read close lseek pread64 readv fstat fstatat newfstatat stat lstat statx statfs fstatfs " +
			"readlink readlinkat getdents64 access faccessat faccessat2 brk mmap mprotect munmap mremap madvise " +
			"rt_sigaction rt_sigprocmask rt_sigreturn sigaltstack set_tid_address set_robust_list rseq futex arch_prctl " +
			"getrandom sched_getaffinity clock_gettime gettimeofday getpid gettid getuid geteuid getgid getegid uname " +
			"exit exit_group execve sched_yield poll ppoll restart_syscall getcwd",
	) {
		if _, ok := info.SyscallNames[name]; ok {
			names = append(names, name)
		}
	}
	var conditions []seccomp.NameWithConditions
	for name, argument := range map[string]uint32{"open": 1, "openat": 2} {
		if _, ok := info.SyscallNames[name]; ok {
			conditions = append(conditions, seccomp.NameWithConditions{Name: name, Conditions: seccomp.ArgumentConditions{{
				Argument: argument, Operation: seccomp.BitsNotSet,
				Value: uint64(unix.O_ACCMODE | unix.O_CREAT | unix.O_TRUNC | unix.O_APPEND | (unix.O_TMPFILE &^ unix.O_DIRECTORY)),
			}}})
		}
	}
	for _, name := range []string{"write", "writev"} {
		for _, fd := range []uint64{1, 2} {
			conditions = append(conditions, seccomp.NameWithConditions{Name: name, Conditions: seccomp.ArgumentConditions{{
				Argument: 0, Operation: seccomp.Equal, Value: fd,
			}}})
		}
	}
	conditions = append(conditions, seccomp.NameWithConditions{Name: "prlimit64", Conditions: seccomp.ArgumentConditions{
		{Argument: 0, Operation: seccomp.Equal, Value: 0},
		{Argument: 2, Operation: seccomp.Equal, Value: 0},
	}})
	filter := seccomp.Filter{NoNewPrivs: true, Policy: seccomp.Policy{
		DefaultAction: seccomp.ActionErrno,
		Syscalls:      []seccomp.SyscallGroup{{Names: names, NamesWithCondtions: conditions, Action: seccomp.ActionAllow}},
	}}
	var triplet, loader string
	switch runtime.GOARCH {
	case "amd64":
		triplet, loader = "x86_64-linux-gnu", "ld-linux-x86-64.so.2"
	case "arm64":
		triplet, loader = "aarch64-linux-gnu", "ld-linux-aarch64.so.1"
	default:
		return errors.New("unsupported memory search architecture")
	}
	rules := []landlock.Rule{
		landlock.PathAccess(ll.AccessFSReadFile|ll.AccessFSReadDir, "."),
		landlock.ROFiles(args[0]),
		landlock.ROFiles("/etc/ld.so.cache").IgnoreIfMissing(),
	}
	for _, dir := range []string{"/lib/" + triplet, "/usr/lib/" + triplet, "/usr/lib"} {
		for _, name := range []string{loader, "libc.so.6", "libm.so.6", "libpthread.so.0", "libgcc_s.so.1", "libpcre2-8.so.0"} {
			rules = append(rules, landlock.ROFiles(filepath.Join(dir, name)).IgnoreIfMissing())
		}
	}
	runtime.LockOSThread()
	if err := landlock.V2.Restrict(rules...); err != nil {
		return err
	}
	if err := seccomp.LoadFilter(filter); err != nil {
		return err
	}
	return unix.Exec(args[0], args, []string{"LANG=C.UTF-8"})
}
