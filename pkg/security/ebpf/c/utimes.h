#ifndef _UTIME_H_
#define _UTIME_H_

#include "syscalls.h"

#include <uapi/linux/utime.h>

/*
  utime syscalls call utimes_common
*/

struct utime_event_t {
    struct kevent_t event;
    struct process_context_t process;
    struct container_context_t container;
    struct syscall_t syscall;
    struct file_t file;
    struct {
        long tv_sec;
        long tv_usec;
    } atime, mtime;
};

int __attribute__((always_inline)) trace__sys_utimes() {
    struct policy_t policy = fetch_policy(EVENT_UTIME);
    if (is_discarded_by_process(policy.mode, EVENT_UTIME)) {
        return 0;
    }

    struct syscall_cache_t syscall = {
        .type = EVENT_UTIME,
        .policy = policy,
    };

    cache_syscall(&syscall);

    return 0;
}

int __attribute__((always_inline)) utime_approvers(struct syscall_cache_t *syscall) {
    return basename_approver(syscall, syscall->setattr.dentry, EVENT_UTIME);
}

// On old kernels, we have sys_utime and compat_sys_utime.
// On new kernels, we have _x64_sys_utime32, __ia32_sys_utime32, __x64_sys_utime, __ia32_sys_utime
SYSCALL_COMPAT_KPROBE0(utime) {
    return trace__sys_utimes();
}

SYSCALL_KPROBE0(utime32) {
    return trace__sys_utimes();
}

SYSCALL_COMPAT_TIME_KPROBE0(utimes) {
    return trace__sys_utimes();
}

SYSCALL_COMPAT_TIME_KPROBE0(utimensat) {
    return trace__sys_utimes();
}

SYSCALL_COMPAT_TIME_KPROBE0(futimesat) {
    return trace__sys_utimes();
}

int __attribute__((always_inline)) do_sys_utimes_ret(void *ctx, struct syscall_cache_t *syscall, int retval) {
    if (IS_UNHANDLED_ERROR(retval))
        return 0;

    struct utime_event_t event = {
        .syscall.retval = retval,
        .atime = {
            .tv_sec = syscall->setattr.atime.tv_sec,
            .tv_usec = syscall->setattr.atime.tv_nsec,
        },
        .mtime = {
            .tv_sec = syscall->setattr.mtime.tv_sec,
            .tv_usec = syscall->setattr.mtime.tv_nsec,
        },
        .file = syscall->setattr.file,
    };

    struct proc_cache_t *entry = fill_process_context(&event.process);
    fill_container_context(entry, &event.container);

    // dentry resolution in setattr.h

    send_event(ctx, EVENT_UTIME, event);

    return 0;
}

SEC("tracepoint/handle_sys_utimes_exit")
int handle_sys_utimes_exit(struct tracepoint_raw_syscalls_sys_exit_t *args) {
    struct syscall_cache_t *syscall = pop_syscall(EVENT_UTIME);
    if (!syscall)
        return 0;

    return do_sys_utimes_ret(args, syscall, args->ret);
}

int __attribute__((always_inline)) trace__sys_utimes_ret(struct pt_regs *ctx) {
    struct syscall_cache_t *syscall = pop_syscall(EVENT_UTIME);
    if (!syscall)
        return 0;

    int retval = PT_REGS_RC(ctx);
    return do_sys_utimes_ret(ctx, syscall, retval);
}

SYSCALL_COMPAT_KRETPROBE(utime) {
    return trace__sys_utimes_ret(ctx);
}

SYSCALL_KRETPROBE(utime32) {
    return trace__sys_utimes_ret(ctx);
}

SYSCALL_COMPAT_TIME_KRETPROBE(utimes) {
    return trace__sys_utimes_ret(ctx);
}

SYSCALL_COMPAT_TIME_KRETPROBE(utimensat) {
    return trace__sys_utimes_ret(ctx);
}

SYSCALL_COMPAT_TIME_KRETPROBE(futimesat) {
    return trace__sys_utimes_ret(ctx);
}

#endif
