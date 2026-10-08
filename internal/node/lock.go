package node

import "github.com/EasySBTeam/EasySB/internal/filelock"

// Locked takes the node lock and returns the store as it is on disk right now.
// The caller changes the store, calls Save and releases the lock. Loading under
// the lock is what keeps a read-modify-write from losing a concurrent change.
func Locked(path string) (*Store, *filelock.Lock, error) {
	lock, err := filelock.Acquire(path)
	if err != nil {
		return nil, nil, err
	}
	store, err := Load(path)
	if err != nil {
		lock.Unlock()
		return nil, nil, err
	}
	return store, lock, nil
}
