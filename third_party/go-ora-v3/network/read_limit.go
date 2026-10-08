package network

import "errors"

// ErrReadLimit means the connection refused a result before allocating its body.
var ErrReadLimit = errors.New("Oracle receive limit exceeded")

// SetReadLimit bounds individual reads, packets, CLR values and LOB buffers.
// Zero retains upstream behavior. Set it before starting a query, without
// concurrent session operations. A rejected connection must be discarded.
func (session *Session) SetReadLimit(limit int) {
	session.ReadLimit = limit
	session.receiveRemaining = limit
}

// CheckReadSize checks an allocation or append without overflowing arithmetic.
func (session *basicSession) CheckReadSize(current, added int) error {
	if current < 0 || added < 0 || current > int(^uint(0)>>1)-added {
		return ErrReadLimit
	}
	if session.ReadLimit > 0 && (current > session.ReadLimit || added > session.ReadLimit-current) {
		return ErrReadLimit
	}
	return nil
}

// consumeReceive runs only when allocating a wire read, not in decode helpers.
func (session *Session) consumeReceive(size int) error {
	if size < 0 {
		return ErrReadLimit
	}
	if session.ReadLimit > 0 {
		if size > session.receiveRemaining {
			return ErrReadLimit
		}
		session.receiveRemaining -= size
	}
	return nil
}
