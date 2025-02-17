#ifndef _SETXATTR_H_
#define _SETXATTR_H_

#include "syscalls.h"

struct setxattr_event_t {
    struct kevent_t event;
    struct process_context_t process;
    struct container_context_t container;
    struct syscall_t syscall;
    struct file_t file;
    char name[MAX_XATTR_NAME_LEN];
};

int __attribute__((always_inline)) trace__sys_setxattr(const char *xattr_name) {
    struct policy_t policy = fetch_policy(EVENT_SETXATTR);
    if (is_discarded_by_process(policy.mode, EVENT_SETXATTR)) {
        return 0;
    }

    struct syscall_cache_t syscall = {
        .type = EVENT_SETXATTR,
        .policy = policy,
        .xattr = {
            .name = xattr_name,
        }
    };

    cache_syscall(&syscall);

    return 0;
}

SYSCALL_KPROBE2(setxattr, const char *, filename, const char *, name) {
    return trace__sys_setxattr(name);
}

SYSCALL_KPROBE2(lsetxattr, const char *, filename, const char *, name) {
    return trace__sys_setxattr(name);
}

SYSCALL_KPROBE2(fsetxattr, int, fd, const char *, name) {
    return trace__sys_setxattr(name);
}

int __attribute__((always_inline)) trace__sys_removexattr(const char *xattr_name) {
    struct policy_t policy = fetch_policy(EVENT_REMOVEXATTR);
    if (is_discarded_by_process(policy.mode, EVENT_REMOVEXATTR)) {
        return 0;
    }

    struct syscall_cache_t syscall = {
        .type = EVENT_REMOVEXATTR,
        .policy = policy,
        .xattr = {
            .name = xattr_name,
        }
    };

    cache_syscall(&syscall);

    return 0;
}

SYSCALL_KPROBE2(removexattr, const char *, filename, const char *, name) {
    return trace__sys_removexattr(name);
}

SYSCALL_KPROBE2(lremovexattr, const char *, filename, const char *, name) {
    return trace__sys_removexattr(name);
}

SYSCALL_KPROBE2(fremovexattr, int, fd, const char *, name) {
    return trace__sys_removexattr(name);
}

int __attribute__((always_inline)) trace__vfs_setxattr(struct pt_regs *ctx, u64 event_type) {
    struct syscall_cache_t *syscall = peek_syscall(event_type);
    if (!syscall)
        return 0;

    if (syscall->xattr.file.path_key.ino) {
        return 0;
    }

    struct dentry *dentry = (struct dentry *)PT_REGS_PARM1(ctx);
    syscall->xattr.dentry = dentry;

    set_file_inode(syscall->xattr.dentry, &syscall->xattr.file, 0);

    // the mount id of path_key is resolved by kprobe/mnt_want_write. It is already set by the time we reach this probe.
    int ret = resolve_dentry(syscall->xattr.dentry, syscall->xattr.file.path_key, syscall->policy.mode != NO_FILTER ? event_type : 0);
    if (ret == DENTRY_DISCARDED) {
        return 0;
    }

    return 0;
}

SEC("kprobe/vfs_setxattr")
int kprobe__vfs_setxattr(struct pt_regs *ctx) {
    return trace__vfs_setxattr(ctx, EVENT_SETXATTR);
}

SEC("kprobe/vfs_removexattr")
int kprobe__vfs_removexattr(struct pt_regs *ctx) {
    return trace__vfs_setxattr(ctx, EVENT_REMOVEXATTR);
}

int __attribute__((always_inline)) _do_sys_setxattr_ret(void *ctx, struct syscall_cache_t *syscall, int retval, u64 event_type) {
    if (IS_UNHANDLED_ERROR(retval))
        return 0;

    struct setxattr_event_t event = {
        .syscall.retval = retval,
        .file = syscall->xattr.file,
    };

    // copy xattr name
    bpf_probe_read_str(&event.name, MAX_XATTR_NAME_LEN, (void*) syscall->xattr.name);

    struct proc_cache_t *entry = fill_process_context(&event.process);
    fill_container_context(entry, &event.container);
    fill_file_metadata(syscall->xattr.dentry, &event.file.metadata);

    send_event(ctx, event_type, event);

    return 0;
}

int __attribute__((always_inline)) do_sys_setxattr_ret(void *ctx, struct syscall_cache_t *syscall, int retval) {
    return _do_sys_setxattr_ret(ctx, syscall, retval, EVENT_SETXATTR);
}

SEC("tracepoint/handle_sys_setxattr_exit")
int handle_sys_setxattr_exit(struct tracepoint_raw_syscalls_sys_exit_t *args) {
    struct syscall_cache_t *syscall = pop_syscall(EVENT_SETXATTR);
    if (!syscall)
        return 0;

    return do_sys_setxattr_ret(args, syscall, args->ret);
}

int __attribute__((always_inline)) do_sys_removexattr_ret(void *ctx, struct syscall_cache_t *syscall, int retval) {
    return _do_sys_setxattr_ret(ctx, syscall, retval, EVENT_REMOVEXATTR);
}

SEC("tracepoint/handle_sys_removexattr_exit")
int handle_sys_removexattr_exit(struct tracepoint_raw_syscalls_sys_exit_t *args) {
    struct syscall_cache_t *syscall = pop_syscall(EVENT_REMOVEXATTR);
    if (!syscall)
        return 0;

    return do_sys_removexattr_ret(args, syscall, args->ret);
}

int __attribute__((always_inline)) trace__sys_setxattr_ret(struct pt_regs *ctx) {
    struct syscall_cache_t *syscall = pop_syscall(EVENT_SETXATTR);
    if (!syscall)
        return 0;

    int retval = PT_REGS_RC(ctx);
    return do_sys_setxattr_ret(ctx, syscall, retval);
}

int __attribute__((always_inline)) trace__sys_removexattr_ret(struct pt_regs *ctx) {
    struct syscall_cache_t *syscall = pop_syscall(EVENT_REMOVEXATTR);
    if (!syscall)
        return 0;

    int retval = PT_REGS_RC(ctx);
    return do_sys_removexattr_ret(ctx, syscall, retval);
}

SYSCALL_KRETPROBE(setxattr) {
    return trace__sys_setxattr_ret(ctx);
}

SYSCALL_KRETPROBE(fsetxattr) {
    return trace__sys_setxattr_ret(ctx);
}

SYSCALL_KRETPROBE(lsetxattr) {
    return trace__sys_setxattr_ret(ctx);
}

SYSCALL_KRETPROBE(removexattr) {
    return trace__sys_removexattr_ret(ctx);
}

SYSCALL_KRETPROBE(lremovexattr) {
    return trace__sys_removexattr_ret(ctx);
}

SYSCALL_KRETPROBE(fremovexattr) {
    return trace__sys_removexattr_ret(ctx);
}

#endif
