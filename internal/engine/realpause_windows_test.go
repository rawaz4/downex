package engine

import "golang.org/x/sys/windows"

// realPause lets about 10 ms of real time pass, also inside a synctest bubble, whose
// time.Sleep is fake: a system call is not a durable block, so the bubble's clock stands
// still meanwhile. The paced harness uses it while the engine waits out another
// process (an anti-virus scan holding the new file), which only real time ends.
func realPause() { windows.SleepEx(10, false) }
