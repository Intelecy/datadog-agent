// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// +build linux

package probe

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/DataDog/datadog-go/statsd"
	lib "github.com/DataDog/ebpf"
	"github.com/DataDog/ebpf/manager"
	"github.com/DataDog/gopsutil/process"
	"github.com/hashicorp/golang-lru/simplelru"
	"github.com/pkg/errors"

	"github.com/DataDog/datadog-agent/pkg/security/metrics"
	"github.com/DataDog/datadog-agent/pkg/security/model"
	"github.com/DataDog/datadog-agent/pkg/security/utils"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

const (
	doForkListInput uint64 = iota
	doForkStructInput
)

const (
	snapshotting = iota
	snapshotted
)

const procResolveMaxDepth = 16

// argsEnvsCacheEntry holds temporary args/envs info
type argsEnvsCacheEntry struct {
	Values      []string
	IsTruncated bool
}

// getDoForkInput returns the expected input type of _do_fork, do_fork and kernel_clone
func getDoForkInput(probe *Probe) uint64 {
	if probe.kernelVersion != 0 && probe.kernelVersion >= kernel5_3 {
		return doForkStructInput
	}
	return doForkListInput
}

// getCGroupWriteConstants returns the value of the constant used to determine how cgroups should be captured in kernel
// space
func getCGroupWriteConstants() manager.ConstantEditor {
	cgroupWriteConst := uint64(1)
	kv, err := NewKernelVersion()
	if err == nil {
		if kv.IsRH7Kernel() {
			cgroupWriteConst = 2
		}
	}

	return manager.ConstantEditor{
		Name:  "cgroup_write_type",
		Value: cgroupWriteConst,
	}
}

// TTYConstants returns the tty constants
func TTYConstants(probe *Probe) []manager.ConstantEditor {
	ttyOffset, nameOffset := uint64(400), uint64(368)

	kv, err := NewKernelVersion()
	if err == nil {
		switch {
		case kv.IsRH7Kernel():
			ttyOffset, nameOffset = 416, 312
		case kv.IsRH8Kernel():
			ttyOffset, nameOffset = 392, 368
		case kv.IsSLES12Kernel():
			ttyOffset, nameOffset = 376, 368
		case kv.IsSLES15Kernel():
			ttyOffset, nameOffset = 408, 368
		case probe.kernelVersion != 0 && probe.kernelVersion < kernel5_3:
			ttyOffset, nameOffset = 368, 368
		}
	}

	return []manager.ConstantEditor{
		{
			Name:  "tty_offset",
			Value: ttyOffset,
		},
		{
			Name:  "tty_name_offset",
			Value: nameOffset,
		},
	}
}

// InodeInfo holds information related to inode from kernel
type InodeInfo struct {
	MountID uint32
	Flags   int32
}

// UnmarshalBinary unmarshals a binary representation of itself
func (i *InodeInfo) UnmarshalBinary(data []byte) (int, error) {
	if len(data) < 8 {
		return 0, model.ErrNotEnoughData
	}
	i.MountID = model.ByteOrder.Uint32(data)
	i.Flags = int32(model.ByteOrder.Uint32(data[4:]))
	return 8, nil
}

// ProcessResolverOpts options of resolver
type ProcessResolverOpts struct {
	DebugCacheSize bool
}

// ProcessResolver resolved process context
type ProcessResolver struct {
	sync.RWMutex
	state        int64
	probe        *Probe
	resolvers    *Resolvers
	client       *statsd.Client
	inodeInfoMap *lib.Map
	procCacheMap *lib.Map
	pidCacheMap  *lib.Map
	cacheSize    int64
	opts         ProcessResolverOpts

	entryCache    map[uint32]*model.ProcessCacheEntry
	argsEnvsCache *simplelru.LRU
}

// SendStats sends process resolver metrics
func (p *ProcessResolver) SendStats() error {
	if err := p.client.Gauge(metrics.MetricProcessResolverCacheSize, p.GetCacheSize(), []string{}, 1.0); err != nil {
		return errors.Wrap(err, "failed to send process_resolver cache_size metric")
	}

	if err := p.client.Gauge(metrics.MetricProcessResolverReferenceCount, p.GetEntryCacheSize(), []string{}, 1.0); err != nil {
		return errors.Wrap(err, "failed to send process_resolver reference_count metric")
	}

	return nil
}

// UpdateArgsEnvs updates arguments or environment variables of the given id
func (p *ProcessResolver) UpdateArgsEnvs(event *model.ArgsEnvsEvent) {
	if e, found := p.argsEnvsCache.Get(event.ID); found {
		entry := e.(*argsEnvsCacheEntry)

		entry.Values = append(entry.Values, event.Values...)
		entry.IsTruncated = entry.IsTruncated || event.IsTruncated
	} else {
		entry := &argsEnvsCacheEntry{
			Values:      event.Values,
			IsTruncated: event.IsTruncated,
		}
		p.argsEnvsCache.Add(event.ID, entry)
	}
}

// AddForkEntry adds an entry to the local cache and returns the newly created entry
func (p *ProcessResolver) AddForkEntry(pid uint32, entry *model.ProcessCacheEntry) *model.ProcessCacheEntry {
	p.Lock()
	defer p.Unlock()
	return p.insertForkEntry(pid, entry)
}

// AddExecEntry adds an entry to the local cache and returns the newly created entry
func (p *ProcessResolver) AddExecEntry(pid uint32, entry *model.ProcessCacheEntry) *model.ProcessCacheEntry {
	p.Lock()
	defer p.Unlock()

	return p.insertExecEntry(pid, entry)
}

// enrichEventFromProc uses /proc to enrich a ProcessCacheEntry with additional metadata
func (p *ProcessResolver) enrichEventFromProc(entry *model.ProcessCacheEntry, proc *process.Process) error {
	filledProc := utils.GetFilledProcess(proc)
	if filledProc == nil {
		return errors.Errorf("snapshot failed for %d: binary was deleted", proc.Pid)
	}

	pid := uint32(proc.Pid)
	// the provided process is a kernel process if its virtual memory size is null
	isKernelProcess := filledProc.MemInfo.VMS == 0

	if !isKernelProcess {
		// Get process filename and pre-fill the cache
		procExecPath := utils.ProcExePath(proc.Pid)
		pathnameStr, err := os.Readlink(procExecPath)
		if err != nil {
			return errors.Wrapf(err, "snapshot failed for %d: couldn't readlink binary", proc.Pid)
		}
		if pathnameStr == "/ (deleted)" {
			return errors.Errorf("snapshot failed for %d: binary was deleted", proc.Pid)
		}

		// Get the inode of the process binary
		fi, err := os.Stat(procExecPath)
		if err != nil {
			return errors.Wrapf(err, "snapshot failed for %d: couldn't stat binary", proc.Pid)
		}
		stat, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.Errorf("snapshot failed for %d: couldn't stat binary", proc.Pid)
		}
		inode := stat.Ino

		info, err := p.retrieveInodeInfo(inode)
		if err != nil {
			return errors.Wrapf(err, "snapshot failed for %d: couldn't retrieve inode info", proc.Pid)
		}

		// Retrieve the container ID of the process from /proc
		containerID, err := p.resolvers.ContainerResolver.GetContainerID(pid)
		if err != nil {
			return errors.Wrapf(err, "snapshot failed for %d: couldn't parse container ID", proc.Pid)
		}

		entry.FileFields = *info
		entry.Process.PathnameStr = pathnameStr
		entry.Process.BasenameStr = path.Base(pathnameStr)
		entry.Process.ContainerID = string(containerID)
		// resolve container path with the MountResolver
		entry.ContainerPath = p.resolvers.resolveContainerPath(&entry.Process.FileFields)
		entry.Filesystem = p.resolvers.MountResolver.GetFilesystem(entry.Process.FileFields.MountID)
	}

	entry.ExecTime = time.Unix(0, filledProc.CreateTime*int64(time.Millisecond))
	entry.ForkTime = entry.ExecTime
	entry.Comm = filledProc.Name
	entry.PPid = uint32(filledProc.Ppid)
	entry.TTYName = utils.PidTTY(filledProc.Pid)
	entry.ProcessContext.Pid = pid
	entry.ProcessContext.Tid = pid
	if len(filledProc.Uids) >= 4 {
		entry.Credentials.UID = uint32(filledProc.Uids[0])
		entry.Credentials.EUID = uint32(filledProc.Uids[1])
		entry.Credentials.FSUID = uint32(filledProc.Uids[3])
	}
	if len(filledProc.Gids) >= 4 {
		entry.Credentials.GID = uint32(filledProc.Gids[0])
		entry.Credentials.EGID = uint32(filledProc.Gids[1])
		entry.Credentials.FSGID = uint32(filledProc.Gids[3])
	}
	var err error
	entry.Credentials.CapEffective, entry.Credentials.CapPermitted, err = utils.CapEffCapEprm(proc.Pid)
	if err != nil {
		return errors.Wrapf(err, "snapshot failed for %d: couldn't parse kernel capabilities", proc.Pid)
	}
	p.SetProcessUsersGroups(entry)

	return nil
}

// retrieveInodeInfo fetches inode metadata from kernel space
func (p *ProcessResolver) retrieveInodeInfo(inode uint64) (*model.FileFields, error) {
	var info model.FileFields
	inodeb := make([]byte, 8)

	model.ByteOrder.PutUint64(inodeb, inode)
	data, err := p.inodeInfoMap.LookupBytes(inodeb)
	if err != nil {
		return nil, err
	}

	if _, err = info.UnmarshalBinary(data); err != nil {
		return nil, err
	}

	if info.Inode == 0 {
		return nil, errors.New("not found")
	}

	return &info, nil
}

func (p *ProcessResolver) insertEntry(pid uint32, entry *model.ProcessCacheEntry) *model.ProcessCacheEntry {
	p.entryCache[pid] = entry

	_ = p.client.Count(metrics.MetricProcessResolverAdded, 1, []string{}, 1.0)

	if p.opts.DebugCacheSize {
		atomic.AddInt64(&p.cacheSize, 1)

		runtime.SetFinalizer(entry, func(obj interface{}) {
			atomic.AddInt64(&p.cacheSize, -1)
		})
	}

	return entry
}

func (p *ProcessResolver) insertForkEntry(pid uint32, entry *model.ProcessCacheEntry) *model.ProcessCacheEntry {
	if prev := p.entryCache[pid]; prev != nil {
		// this shouldn't happen but it is better to exit the prev and let the new one replace it
		prev.Exit(entry.ForkTime)
	}

	parent := p.entryCache[entry.PPid]
	if parent != nil {
		parent.Fork(entry)
	}

	return p.insertEntry(pid, entry)
}

func (p *ProcessResolver) insertExecEntry(pid uint32, entry *model.ProcessCacheEntry) *model.ProcessCacheEntry {
	if prev := p.entryCache[pid]; prev != nil {
		prev.Exec(entry)
	}

	return p.insertEntry(pid, entry)
}

func (p *ProcessResolver) deleteEntry(pid uint32, exitTime time.Time) {
	// Start by updating the exit timestamp of the pid cache entry
	entry, ok := p.entryCache[pid]
	if !ok {
		return
	}
	entry.Exit(exitTime)
	delete(p.entryCache, entry.Pid)
}

// DeleteEntry tries to delete an entry in the process cache
func (p *ProcessResolver) DeleteEntry(pid uint32, exitTime time.Time) {
	p.Lock()
	defer p.Unlock()

	p.deleteEntry(pid, exitTime)
}

// Resolve returns the cache entry for the given pid
func (p *ProcessResolver) Resolve(pid, tid uint32) *model.ProcessCacheEntry {
	p.Lock()
	defer p.Unlock()

	entry, exists := p.entryCache[pid]
	if exists {
		_ = p.client.Count(metrics.MetricProcessResolverCacheHits, 1, []string{"type:cache"}, 1.0)
		return entry
	}

	if atomic.LoadInt64(&p.state) != snapshotted {
		return nil
	}

	// fallback to the kernel maps directly, the perf event may be delayed / may have been lost
	if entry = p.resolveWithKernelMaps(pid, tid); entry != nil {
		_ = p.client.Count(metrics.MetricProcessResolverCacheHits, 1, []string{"type:kernel_maps"}, 1.0)
		return entry
	}

	// fallback to /proc, the in-kernel LRU may have deleted the entry
	if entry = p.resolveWithProcfs(pid, procResolveMaxDepth); entry != nil {
		_ = p.client.Count(metrics.MetricProcessResolverCacheHits, 1, []string{"type:procfs"}, 1.0)
		return entry
	}

	_ = p.client.Count(metrics.MetricProcessResolverCacheMiss, 1, []string{}, 1.0)
	return nil
}

// SetProcessPath resolves process file path
func (p *ProcessResolver) SetProcessPath(entry *model.ProcessCacheEntry) (string, error) {
	var err error

	if entry.FileFields.Inode != 0 && entry.FileFields.MountID != 0 {
		if entry.PathnameStr, err = p.resolvers.resolveFileFieldsPath(&entry.FileFields); err == nil {
			entry.BasenameStr = path.Base(entry.PathnameStr)
		}
	}

	return entry.PathnameStr, err
}

// SetProcessFilesystem resolves process file system
func (p *ProcessResolver) SetProcessFilesystem(entry *model.ProcessCacheEntry) string {
	if entry.FileFields.MountID != 0 {
		entry.Filesystem = p.resolvers.MountResolver.GetFilesystem(entry.FileFields.MountID)
	}

	return entry.Filesystem
}

// SetProcessContainerPath resolves container path
func (p *ProcessResolver) SetProcessContainerPath(entry *model.ProcessCacheEntry) string {
	if entry.FileFields.Inode != 0 && entry.FileFields.MountID != 0 {
		entry.ContainerPath = p.resolvers.resolveContainerPath(&entry.FileFields)
	}
	return entry.ContainerPath
}

func (p *ProcessResolver) unmarshalFromKernelMaps(entry *model.ProcessCacheEntry, data []byte) (int, error) {
	// unmarshal container ID first
	id, err := model.UnmarshalString(data, 64)
	if err != nil {
		return 0, err
	}
	entry.ContainerID = id

	read, err := entry.UnmarshalBinary(data[64:])
	if err != nil {
		return read + 64, err
	}

	entry.ExecTime = p.resolvers.TimeResolver.ResolveMonotonicTimestamp(entry.ExecTimestamp)
	entry.ForkTime = p.resolvers.TimeResolver.ResolveMonotonicTimestamp(entry.ForkTimestamp)
	entry.ExitTime = p.resolvers.TimeResolver.ResolveMonotonicTimestamp(entry.ExitTimestamp)

	return read + 64, err
}

func (p *ProcessResolver) resolveWithKernelMaps(pid, tid uint32) *model.ProcessCacheEntry {
	pidb := make([]byte, 4)
	model.ByteOrder.PutUint32(pidb, pid)

	cookieb, err := p.pidCacheMap.LookupBytes(pidb)
	if err != nil || cookieb == nil {
		return nil
	}

	// first 4 bytes are the actual cookie
	entryb, err := p.procCacheMap.LookupBytes(cookieb[0:4])
	if err != nil || entryb == nil {
		return nil
	}

	entry := NewProcessCacheEntry()
	data := append(entryb, cookieb...)

	if _, err = p.unmarshalFromKernelMaps(entry, data); err != nil {
		return nil
	}
	entry.Pid = pid
	entry.Tid = tid

	// If we fall back to the kernel maps for a process in a container that was already running when the agent
	// started, the kernel space container ID will be empty even though the process is inside a container. Since there
	// is no insurance that the parent of this process is still running, we can't use our user space cache to check if
	// the parent is in a container. In other words, we have to fall back to /proc to query the container ID of the
	// process.
	if entry.ContainerID == "" {
		containerID, err := p.resolvers.ContainerResolver.GetContainerID(pid)
		if err == nil {
			entry.ContainerID = string(containerID)
		}
	}

	if entry.ExecTime.IsZero() {
		return p.insertForkEntry(pid, entry)
	}

	return p.insertExecEntry(pid, entry)
}

func (p *ProcessResolver) resolveWithProcfs(pid uint32, maxDepth int) *model.ProcessCacheEntry {
	if maxDepth < 1 || pid == 0 {
		return nil
	}

	proc, err := process.NewProcess(int32(pid))
	if err != nil {
		return nil
	}

	filledProc := utils.GetFilledProcess(proc)
	if filledProc == nil {
		return nil
	}

	parent := p.resolveWithProcfs(uint32(filledProc.Ppid), maxDepth-1)
	entry, inserted := p.syncCache(proc)
	if inserted && entry != nil {
		entry.Ancestor = parent
	}

	return entry
}

// SetProcessArgs set arguments to cache entry
func (p *ProcessResolver) SetProcessArgs(pce *model.ProcessCacheEntry) {
	if e, found := p.argsEnvsCache.Get(pce.ArgsID); found {
		entry := e.(*argsEnvsCacheEntry)

		pce.ArgsArray = entry.Values
		if pce.ArgsTruncated {
			pce.ArgsArray = append(pce.ArgsArray, "...")
		}
		pce.ArgsTruncated = pce.ArgsTruncated || entry.IsTruncated
	}
}

// SetProcessEnvs set environment variables to cache entry
func (p *ProcessResolver) SetProcessEnvs(pce *model.ProcessCacheEntry) {
	if e, found := p.argsEnvsCache.Get(pce.EnvsID); found {
		entry := e.(*argsEnvsCacheEntry)

		// keep only keys
		pce.EnvsArray = make([]string, len(entry.Values))
		for i, env := range entry.Values {
			if els := strings.SplitN(env, "=", 2); len(els) > 0 {
				pce.EnvsArray[i] = els[0]
			}
		}

		if pce.EnvsTruncated {
			pce.EnvsArray = append(pce.EnvsArray, "...")
		}
		pce.EnvsTruncated = pce.EnvsTruncated || entry.IsTruncated
	}
}

// SetProcessTTY resolves TTY and cache the result
func (p *ProcessResolver) SetProcessTTY(pce *model.ProcessCacheEntry) string {
	if pce.TTYName == "" {
		tty := utils.PidTTY(int32(pce.Pid))
		pce.TTYName = tty
	}
	return pce.TTYName
}

// SetProcessUsersGroups resolves and set users and groups
func (p *ProcessResolver) SetProcessUsersGroups(pce *model.ProcessCacheEntry) {
	pce.User, _ = p.resolvers.UserGroupResolver.ResolveUser(int(pce.Credentials.UID))
	pce.EUser, _ = p.resolvers.UserGroupResolver.ResolveUser(int(pce.Credentials.EUID))
	pce.FSUser, _ = p.resolvers.UserGroupResolver.ResolveUser(int(pce.Credentials.FSUID))

	pce.Group, _ = p.resolvers.UserGroupResolver.ResolveGroup(int(pce.Credentials.GID))
	pce.EGroup, _ = p.resolvers.UserGroupResolver.ResolveGroup(int(pce.Credentials.EGID))
	pce.FSGroup, _ = p.resolvers.UserGroupResolver.ResolveGroup(int(pce.Credentials.FSGID))
}

// Get returns the cache entry for a specified pid
func (p *ProcessResolver) Get(pid uint32) *model.ProcessCacheEntry {
	p.RLock()
	defer p.RUnlock()
	return p.entryCache[pid]
}

// UpdateUID updates the credentials of the provided pid
func (p *ProcessResolver) UpdateUID(pid uint32, e *Event) {
	if e.ProcessContext.Pid != e.ProcessContext.Tid {
		return
	}

	p.Lock()
	defer p.Unlock()
	entry := p.entryCache[pid]
	if entry != nil {
		entry.Credentials.UID = e.SetUID.UID
		entry.Credentials.User = e.ResolveSetuidUser(&e.SetUID)
		entry.Credentials.EUID = e.SetUID.EUID
		entry.Credentials.EUser = e.ResolveSetuidEUser(&e.SetUID)
		entry.Credentials.FSUID = e.SetUID.FSUID
		entry.Credentials.FSUser = e.ResolveSetuidFSUser(&e.SetUID)
	}
}

// UpdateGID updates the credentials of the provided pid
func (p *ProcessResolver) UpdateGID(pid uint32, e *Event) {
	if e.ProcessContext.Pid != e.ProcessContext.Tid {
		return
	}

	p.Lock()
	defer p.Unlock()
	entry := p.entryCache[pid]
	if entry != nil {
		entry.Credentials.GID = e.SetGID.GID
		entry.Credentials.Group = e.ResolveSetgidGroup(&e.SetGID)
		entry.Credentials.EGID = e.SetGID.EGID
		entry.Credentials.EGroup = e.ResolveSetgidEGroup(&e.SetGID)
		entry.Credentials.FSGID = e.SetGID.FSGID
		entry.Credentials.FSGroup = e.ResolveSetgidFSGroup(&e.SetGID)
	}
}

// UpdateCapset updates the credentials of the provided pid
func (p *ProcessResolver) UpdateCapset(pid uint32, e *Event) {
	if e.ProcessContext.Pid != e.ProcessContext.Tid {
		return
	}

	p.Lock()
	defer p.Unlock()
	entry := p.entryCache[pid]
	if entry != nil {
		entry.Credentials.CapEffective = e.Capset.CapEffective
		entry.Credentials.CapPermitted = e.Capset.CapPermitted
	}
}

// Start starts the resolver
func (p *ProcessResolver) Start(ctx context.Context) error {
	var err error
	if p.inodeInfoMap, err = p.probe.Map("inode_info_cache"); err != nil {
		return err
	}

	if p.procCacheMap, err = p.probe.Map("proc_cache"); err != nil {
		return err
	}

	if p.pidCacheMap, err = p.probe.Map("pid_cache"); err != nil {
		return err
	}

	go p.cacheFlush(ctx)

	return nil
}

func (p *ProcessResolver) cacheFlush(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case now := <-ticker.C:
			var pids []uint32

			p.RLock()
			for pid := range p.entryCache {
				pids = append(pids, pid)
			}
			p.RUnlock()

			delEntry := func(pid uint32, exitTime time.Time) {
				p.deleteEntry(pid, exitTime)
				_ = p.client.Count(metrics.MetricProcessResolverFlushed, 1, []string{}, 1.0)
			}

			// flush slowly
			for _, pid := range pids {
				if _, err := process.NewProcess(int32(pid)); err != nil {
					// check start time to ensure to not delete a recent pid
					p.Lock()
					if entry := p.entryCache[pid]; entry != nil {
						if tm := entry.ExecTime; !tm.IsZero() && tm.Add(time.Minute).Before(now) {
							delEntry(pid, now)
						} else if tm := entry.ForkTime; !tm.IsZero() && tm.Add(time.Minute).Before(now) {
							delEntry(pid, now)
						} else if entry.ForkTime.IsZero() && entry.ExecTime.IsZero() {
							delEntry(pid, now)
						}
					}
					p.Unlock()
				}
				time.Sleep(50 * time.Millisecond)
			}
		case <-ctx.Done():
			return
		}
	}
}

// SyncCache snapshots /proc for the provided pid. This method returns true if it updated the process cache.
func (p *ProcessResolver) SyncCache(proc *process.Process) bool {
	// Only a R lock is necessary to check if the entry exists, but if it exists, we'll update it, so a RW lock is
	// required.
	p.Lock()
	defer p.Unlock()
	_, ret := p.syncCache(proc)
	return ret
}

// syncCache snapshots /proc for the provided pid. This method returns true if it updated the process cache.
func (p *ProcessResolver) syncCache(proc *process.Process) (*model.ProcessCacheEntry, bool) {
	pid := uint32(proc.Pid)

	// Check if an entry is already in cache for the given pid.
	entry := p.entryCache[pid]
	if entry != nil {
		return nil, false
	}

	entry = NewProcessCacheEntry()

	// update the cache entry
	if err := p.enrichEventFromProc(entry, proc); err != nil {
		log.Debug(err)
		return nil, false
	}

	parent := p.entryCache[entry.PPid]
	if parent != nil {
		entry.Ancestor = parent
	}

	if entry = p.insertEntry(pid, entry); entry == nil {
		return nil, false
	}

	log.Tracef("New process cache entry added: %s %s %d/%d", entry.Comm, entry.PathnameStr, pid, entry.FileFields.Inode)

	return entry, true
}

func (p *ProcessResolver) dumpEntry(writer io.Writer, entry *model.ProcessCacheEntry, already map[string]bool) {
	for entry != nil {
		label := fmt.Sprintf("%s:%d", entry.Comm, entry.Pid)
		if _, exists := already[label]; !exists {
			if !entry.ExitTime.IsZero() {
				label = "[" + label + "]"
			}

			fmt.Fprintf(writer, `"%d:%s" [label="%s"];`, entry.Pid, entry.Comm, label)
			fmt.Fprintln(writer)

			already[label] = true
		}

		if entry.Ancestor != nil {
			relation := fmt.Sprintf(`"%d:%s" -> "%d:%s";`, entry.Ancestor.Pid, entry.Ancestor.Comm, entry.Pid, entry.Comm)
			if _, exists := already[relation]; !exists {
				fmt.Fprintln(writer, relation)

				already[relation] = true
			}
		}

		entry = entry.Ancestor
	}
}

// Dump create a temp file and dump the cache
func (p *ProcessResolver) Dump() (string, error) {
	dump, err := ioutil.TempFile("/tmp", "process-cache-dump-")
	if err != nil {
		return "", err
	}
	defer dump.Close()

	if err := os.Chmod(dump.Name(), 0400); err != nil {
		return "", err
	}

	p.RLock()
	defer p.RUnlock()

	fmt.Fprintf(dump, "digraph ProcessTree {\n")

	already := make(map[string]bool)
	for _, entry := range p.entryCache {
		p.dumpEntry(dump, entry, already)
	}

	fmt.Fprintf(dump, `}`)

	return dump.Name(), err
}

// GetCacheSize returns the cache size of the process resolver
func (p *ProcessResolver) GetCacheSize() float64 {
	p.RLock()
	defer p.RUnlock()
	return float64(len(p.entryCache))
}

// GetEntryCacheSize returns the cache size of the process resolver
func (p *ProcessResolver) GetEntryCacheSize() float64 {
	return float64(atomic.LoadInt64(&p.cacheSize))
}

// SetState sets the process resolver state
func (p *ProcessResolver) SetState(state int64) {
	atomic.StoreInt64(&p.state, state)
}

// NewProcessResolver returns a new process resolver
func NewProcessResolver(probe *Probe, resolvers *Resolvers, client *statsd.Client, opts ProcessResolverOpts) (*ProcessResolver, error) {
	argsEnvsCache, err := simplelru.NewLRU(512, nil)
	if err != nil {
		return nil, err
	}

	return &ProcessResolver{
		probe:         probe,
		resolvers:     resolvers,
		client:        client,
		entryCache:    make(map[uint32]*model.ProcessCacheEntry),
		opts:          opts,
		argsEnvsCache: argsEnvsCache,
		state:         snapshotting,
	}, nil
}

// NewProcessResolverOpts returns a new set of process resolver options
func NewProcessResolverOpts(debug bool, cookieCacheSize int) ProcessResolverOpts {
	return ProcessResolverOpts{
		DebugCacheSize: debug,
	}
}
