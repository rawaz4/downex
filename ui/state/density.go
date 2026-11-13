package state

import "sync"

// DensityChoice is the list density (DESIGN-SYSTEM.md §2.3): Comfortable (two lines per
// download, the default) or Compact (one line).
type DensityChoice string

// The densities.
const (
	Comfortable DensityChoice = "comfortable"
	Compact     DensityChoice = "compact"
)

// Densities lists them in order.
func Densities() []DensityChoice { return []DensityChoice{Comfortable, Compact} }

// ParseDensity reads anything but "compact" as Comfortable.
func ParseDensity(v string) DensityChoice {
	if v == string(Compact) {
		return Compact
	}
	return Comfortable
}

// Density is the per-viewer list density (stores/density.svelte.ts), stored under
// DensityKey. A storage failure leaves the choice for the session and is reported by
// StorageErr.
type Density struct {
	Changes Signal

	storage  Storage
	unwrites func()
	mu       sync.Mutex
	choice   DensityChoice
	writeErr error // the last synchronous Set's
}

// NewDensity reads the stored choice (storage nil: none).
func NewDensity(storage Storage) *Density {
	d := &Density{storage: storage, choice: Comfortable}
	if storage != nil {
		if v, _, err := storage.Get(DensityKey); err == nil {
			d.choice = ParseDensity(v)
		}
	}
	d.unwrites = watchWrites(storage, d.Changes.Notify)
	return d
}

// Close stops following the storage's write status.
func (d *Density) Close() {
	d.mu.Lock()
	unwrites := d.unwrites
	d.unwrites = nil
	d.mu.Unlock()
	if unwrites != nil {
		unwrites()
	}
}

// StorageErr reports why the choice is not stored: the storage's error, nil when it is
// stored. A change of it is notified through Changes.
func (d *Density) StorageErr() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return storageErr(d.storage, d.writeErr)
}

// Choice is the density.
func (d *Density) Choice() DensityChoice { d.mu.Lock(); defer d.mu.Unlock(); return d.choice }

// Compact reports that compact rows are shown.
func (d *Density) Compact() bool { return d.Choice() == Compact }

// Set chooses and stores a density. A storage that writes later (WriteStatus) never makes it
// wait; a failure is reported by StorageErr, and the choice lasts for this session either
// way.
func (d *Density) Set(c DensityChoice) {
	c = ParseDensity(string(c))
	d.mu.Lock()
	d.choice = c
	d.mu.Unlock()
	if d.storage != nil {
		err := d.storage.Set(DensityKey, string(c))
		d.mu.Lock()
		d.writeErr = err
		d.mu.Unlock()
	}
	d.Changes.Notify()
}

// Toggle switches between the two.
func (d *Density) Toggle() {
	if d.Choice() == Compact {
		d.Set(Comfortable)
	} else {
		d.Set(Compact)
	}
}
