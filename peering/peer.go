package peering

import (
	"time"
)

type evidenceFun func(p *Peer)

func (p *Peer) isAlive() bool {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	return p._isAlive()
}

func (p *Peer) _isAlive() bool {
	// peer is alive if its last activity is at least some heartbeats old
	return time.Since(p.lastActivity) < aliveDuration
}

func (p *Peer) HasTxStore() bool {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	return p.hasTxStore
}

func (p *Peer) evidence(evidences ...evidenceFun) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	for _, fun := range evidences {
		fun(p)
	}
}

func evidenceAndLogActivity(env Environment, evidenceSource string) evidenceFun {
	return func(p *Peer) {
		if !p._isAlive() {
			env.Log().Infof("peering: connected to peer %s (%s) (%s)", ShortPeerIDString(p.id), p.name, evidenceSource)
		}
		p.lastActivity = time.Now()
		p.needsLogLostConnection = true
	}
}

func evidenceTxStore(hasTxStore bool) evidenceFun {
	return func(p *Peer) {
		p.hasTxStore = hasTxStore
	}
}

func evidenceClockDifference(diff time.Duration) evidenceFun {
	return func(p *Peer) {
		// store in the ring buffer
		p.clockDifferences[p.clockDifferencesIdx] = diff
		p.clockDifferencesIdx = (p.clockDifferencesIdx + 1) % len(p.clockDifferences)
	}
}

func evidenceIncoming(good bool) evidenceFun {
	if good {
		return func(p *Peer) {
			p.incomingGood++
		}
	}
	return func(p *Peer) {
		p.incomingBad++
	}
}

func (p *Peer) avgClockDifference() time.Duration {
	var ret time.Duration

	for _, d := range p.clockDifferences {
		ret += d
	}
	return ret / time.Duration(len(p.clockDifferences))
}
