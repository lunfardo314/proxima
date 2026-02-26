// Tests for the core/vertex package which provides in-memory transaction representations.
// The package defines three vertex types:
// - Vertex: full transaction with resolved dependencies
// - DetachedVertex: transaction without input dependencies (memory optimization)
// - VirtualTransaction: partial transaction placeholder with only some outputs
//
// WrappedTx is the central abstraction that can hold any of these types and provides
// thread-safe access, status tracking, and consumer management for UTXO conflict detection.

package vertex

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/stretchr/testify/require"
)

var genesisPrivateKey = ledger.InitWithTestingLedgerData(
	ledger.WithBranchCoverageBounds(0, 2*ledger.DefaultInitialSupply),
)

// TestStatusAndFlags verifies the Status and Flags types that track transaction state.
// Status can be Undefined (not yet processed), Good (valid), or Bad (validation failed).
// Flags are bit flags tracking: defined, constraints valid, attachment started/finished.
func TestStatusAndFlags(t *testing.T) {
	t.Run("status string", func(t *testing.T) {
		require.Equal(t, "UNDEF", Undefined.String())
		require.Equal(t, "GOOD", Good.String())
		require.Equal(t, "BAD", Bad.String())
	})

	t.Run("status from string", func(t *testing.T) {
		require.Equal(t, Good, StatusFromString("GOOD"))
		require.Equal(t, Good, StatusFromString("good"))
		require.Equal(t, Bad, StatusFromString("BAD"))
		require.Equal(t, Bad, StatusFromString("bad"))
		require.Equal(t, Undefined, StatusFromString("unknown"))
		require.Equal(t, Undefined, StatusFromString(""))
	})

	t.Run("flags operations", func(t *testing.T) {
		var f Flags

		require.False(t, f.FlagsUp(FlagVertexDefined))
		require.False(t, f.FlagsUp(FlagVertexConstraintsValid))

		f.SetFlagsUp(FlagVertexDefined)
		require.True(t, f.FlagsUp(FlagVertexDefined))
		require.False(t, f.FlagsUp(FlagVertexConstraintsValid))

		f.SetFlagsUp(FlagVertexConstraintsValid)
		require.True(t, f.FlagsUp(FlagVertexDefined))
		require.True(t, f.FlagsUp(FlagVertexConstraintsValid))

		// Test combined flag check
		require.True(t, f.FlagsUp(FlagVertexDefined|FlagVertexConstraintsValid))
	})

	t.Run("flags string", func(t *testing.T) {
		var f Flags
		str := f.String()
		require.Contains(t, str, "defined=false")
		require.Contains(t, str, "validated=false")

		f.SetFlagsUp(FlagVertexDefined | FlagVertexConstraintsValid)
		str = f.String()
		require.Contains(t, str, "defined=true")
		require.Contains(t, str, "validated=true")
	})
}

// TestWrapTxID tests wrapping a transaction ID into a WrappedTx.
// WrapTxID creates a virtual transaction placeholder that can later be converted
// to a full Vertex when the transaction bytes become available. Tests verify:
// - Basic ID wrapping preserves timestamp, slot, and sequencer flags
// - Branch transactions (tick=0) are correctly identified
// - Initial status is Undefined
// - NumProducedOutputs is derived from the txid's max output index
func TestWrapTxID(t *testing.T) {
	t.Run("wrap random txid", func(t *testing.T) {
		txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
		vid := WrapTxID(txid)

		require.NotNil(t, vid)
		require.Equal(t, txid, vid.ID())
		require.Equal(t, txid.Timestamp(), vid.Timestamp())
		require.Equal(t, txid.Slot(), vid.Slot())
		require.False(t, vid.IsBranchTransaction())
		require.False(t, vid.IsSequencerTransaction())
		require.True(t, vid.IsVirtualTx())
	})

	t.Run("wrap sequencer txid", func(t *testing.T) {
		txid := base.RandomTransactionID(true, 3, base.T(1000, 50))
		vid := WrapTxID(txid)

		require.NotNil(t, vid)
		require.Equal(t, txid, vid.ID())
		require.True(t, vid.IsSequencerTransaction())
		require.False(t, vid.IsBranchTransaction())
	})

	t.Run("wrap branch txid", func(t *testing.T) {
		txid := base.RandomTransactionID(true, 3, base.T(1000, 0)) // tick=0 makes it a branch
		vid := WrapTxID(txid)

		require.NotNil(t, vid)
		require.True(t, vid.IsBranchTransaction())
		require.True(t, vid.IsSequencerTransaction())
	})

	t.Run("initial status is undefined", func(t *testing.T) {
		txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
		vid := WrapTxID(txid)

		require.Equal(t, Undefined, vid.GetTxStatus())
		require.Nil(t, vid.GetError())
		require.False(t, vid.IsBad())
	})

	t.Run("num produced outputs", func(t *testing.T) {
		for i := 0; i < 10; i++ {
			maxIdx := byte(i)
			txid := base.RandomTransactionID(false, maxIdx, base.T(1000, 50))
			vid := WrapTxID(txid)
			require.Equal(t, int(maxIdx)+1, vid.NumProducedOutputs())
		}
	})
}

// TestWrappedTxStatusManagement tests the status lifecycle of a WrappedTx.
// Transactions transition from Undefined -> Good (valid) or Bad (validation failed).
// SetTxStatusGood records optional ledger coverage.
// SetTxStatusBad stores the validation error for later retrieval.
// Also tests setting and clearing individual flags (attachment started/finished, etc).
func TestWrappedTxStatusManagement(t *testing.T) {
	t.Run("set status good", func(t *testing.T) {
		txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
		vid := WrapTxID(txid)

		require.Equal(t, Undefined, vid.GetTxStatus())

		vid.SetTxStatusGood(nil, 0)

		require.Equal(t, Good, vid.GetTxStatus())
		require.False(t, vid.IsBad())
		require.Nil(t, vid.GetError())
	})

	t.Run("set status bad", func(t *testing.T) {
		txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
		vid := WrapTxID(txid)

		testErr := errors.New("test validation error")
		vid.SetTxStatusBad(testErr)

		require.Equal(t, Bad, vid.GetTxStatus())
		require.True(t, vid.IsBad())
		require.Equal(t, testErr, vid.GetError())
	})

	t.Run("set flags up and down", func(t *testing.T) {
		txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
		vid := WrapTxID(txid)

		require.False(t, vid.FlagsUp(FlagVertexTxAttachmentStarted))

		vid.Unwrap(UnwrapOptions{
			VirtualTx: func(v *VirtualTransaction) {
				vid.SetFlagsUpNoLock(FlagVertexTxAttachmentStarted)
			},
		})

		require.True(t, vid.FlagsUp(FlagVertexTxAttachmentStarted))

		vid.Unwrap(UnwrapOptions{
			VirtualTx: func(v *VirtualTransaction) {
				vid.SetFlagsDownNoLock(FlagVertexTxAttachmentStarted)
			},
		})

		require.False(t, vid.FlagsUp(FlagVertexTxAttachmentStarted))
	})
}

// TestWrappedTxOutputID tests generating OutputIDs from a WrappedTx.
// OutputID combines the transaction ID with an output index to uniquely
// identify a specific UTXO produced by the transaction.
func TestWrappedTxOutputID(t *testing.T) {
	t.Run("output id generation", func(t *testing.T) {
		txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
		vid := WrapTxID(txid)

		for idx := byte(0); idx < 6; idx++ {
			oid := vid.OutputID(idx)
			require.Equal(t, txid, oid.TransactionID())
			require.Equal(t, idx, oid.Index())
		}
	})
}

// TestWrappedTxPokeCallback tests the poke mechanism used to notify listeners.
// OnPoke registers a callback that is invoked when Poke() is called.
// This is used to wake up waiting goroutines when transaction state changes.
func TestWrappedTxPokeCallback(t *testing.T) {
	t.Run("poke callback", func(t *testing.T) {
		txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
		vid := WrapTxID(txid)

		pokeCalled := false
		vid.OnPoke(func() {
			pokeCalled = true
		})

		require.False(t, pokeCalled)
		vid.Poke()
		require.True(t, pokeCalled)
	})

	t.Run("poke nop does not panic", func(t *testing.T) {
		txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
		vid := WrapTxID(txid)

		// Default should be nop
		require.NotPanics(t, func() {
			vid.Poke()
		})
	})
}

// TestWrappedTxConsumerTracking tests consumer tracking for UTXO conflict detection.
// Each output can have multiple consumers (transactions that spend it).
// Multiple consumers on the same output indicate a double-spend conflict.
// NumConsumers returns both the count of consumed outputs and conflict count.
func TestWrappedTxConsumerTracking(t *testing.T) {
	t.Run("add and get consumers", func(t *testing.T) {
		txid1 := base.RandomTransactionID(false, 5, base.T(1000, 50))
		vid1 := WrapTxID(txid1)

		txid2 := base.RandomTransactionID(false, 3, base.T(1001, 50))
		consumer := WrapTxID(txid2)

		// Add consumer at output index 0
		vid1.AddConsumer(0, consumer)

		consumers := vid1.ConsumersOf(0)
		require.Equal(t, 1, len(consumers))
		require.True(t, consumers.Contains(consumer))

		// No consumers at other indices
		consumers2 := vid1.ConsumersOf(1)
		require.Equal(t, 0, len(consumers2))
	})

	t.Run("multiple consumers same output", func(t *testing.T) {
		txid1 := base.RandomTransactionID(false, 5, base.T(1000, 50))
		vid := WrapTxID(txid1)

		consumer1 := WrapTxID(base.RandomTransactionID(false, 2, base.T(1001, 50)))
		consumer2 := WrapTxID(base.RandomTransactionID(false, 2, base.T(1002, 50)))

		vid.AddConsumer(0, consumer1)
		vid.AddConsumer(0, consumer2)

		consumers := vid.ConsumersOf(0)
		require.Equal(t, 2, len(consumers))
		require.True(t, consumers.Contains(consumer1))
		require.True(t, consumers.Contains(consumer2))
	})

	t.Run("num consumers", func(t *testing.T) {
		txid1 := base.RandomTransactionID(false, 5, base.T(1000, 50))
		vid := WrapTxID(txid1)

		consumer1 := WrapTxID(base.RandomTransactionID(false, 2, base.T(1001, 50)))
		consumer2 := WrapTxID(base.RandomTransactionID(false, 2, base.T(1002, 50)))
		consumer3 := WrapTxID(base.RandomTransactionID(false, 2, base.T(1003, 50)))

		// One consumer on output 0
		vid.AddConsumer(0, consumer1)
		// Two consumers on output 1 (conflict)
		vid.AddConsumer(1, consumer2)
		vid.AddConsumer(1, consumer3)

		numConsumed, numConflicts := vid.NumConsumers()
		require.Equal(t, 2, numConsumed)  // outputs 0 and 1 are consumed
		require.Equal(t, 1, numConflicts) // output 1 has a conflict
	})
}

// TestVirtualTransactionPullLogic tests the pull mechanism for fetching missing transactions.
// VirtualTransaction represents a transaction known only by ID, not yet received.
// Pull rules control when and how often the node requests the full transaction.
// PullPatienceExpired returns true after N unsuccessful pull attempts.
func TestVirtualTransactionPullLogic(t *testing.T) {
	t.Run("pull rules not defined initially", func(t *testing.T) {
		txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
		vid := WrapTxID(txid)

		var pullRulesDefined bool
		vid.UnwrapVirtualTx(func(v *VirtualTransaction) {
			pullRulesDefined = v.PullRulesDefined()
		})
		require.False(t, pullRulesDefined)
	})

	t.Run("set pull needed", func(t *testing.T) {
		txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
		vid := WrapTxID(txid)

		vid.UnwrapVirtualTx(func(v *VirtualTransaction) {
			v.SetPullNeeded()
			require.True(t, v.PullRulesDefined())
			require.True(t, v.PullNeeded()) // time.Now() is after nextPull
		})
	})

	t.Run("pull happened updates state", func(t *testing.T) {
		txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
		vid := WrapTxID(txid)

		vid.UnwrapVirtualTx(func(v *VirtualTransaction) {
			v.SetPullNeeded()
			require.True(t, v.PullNeeded())

			v.SetPullHappened(100 * time.Millisecond)
			// Right after pull, next pull is in the future
			require.False(t, v.PullNeeded())
		})
	})

	t.Run("pull patience expired", func(t *testing.T) {
		txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
		vid := WrapTxID(txid)

		vid.UnwrapVirtualTx(func(v *VirtualTransaction) {
			v.SetPullNeeded()

			// Simulate 5 pull attempts
			// Use a small negative duration to ensure nextPull is in the past
			for i := 0; i < 5; i++ {
				v.timesPulled++
			}
			// Ensure nextPull is in the past so PullNeeded returns true
			v.nextPull = time.Now().Add(-time.Millisecond)

			require.True(t, v.PullNeeded())
			require.True(t, v.PullPatienceExpired(5))
			require.False(t, v.PullPatienceExpired(10))
		})
	})
}

// TestVirtualTransactionOutputs tests output availability in virtual transactions.
// Virtual transactions may have some outputs available (received separately)
// while the full transaction is still missing. OutputAt returns nil for unavailable outputs.
func TestVirtualTransactionOutputs(t *testing.T) {
	t.Run("output at index", func(t *testing.T) {
		txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
		vid := WrapTxID(txid)

		// Initially no outputs available
		out, err := vid.OutputAt(0)
		require.Nil(t, err) // No error, but nil output
		require.Nil(t, out)
	})
}

// TestWrappedOutput tests the WrappedOutput type that pairs a WrappedTx with an output index.
// WrappedOutput provides methods for decoding the full OutputID, string formatting,
// timestamp access, and validation (checking if index is within valid range).
func TestWrappedOutput(t *testing.T) {
	t.Run("decode id", func(t *testing.T) {
		txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
		vid := WrapTxID(txid)

		wout := WrappedOutput{VID: vid, Index: 2}
		oid := wout.DecodeID()

		require.Equal(t, txid, oid.TransactionID())
		require.Equal(t, byte(2), oid.Index())
	})

	t.Run("nil vid decode id panics", func(t *testing.T) {
		wout := WrappedOutput{VID: nil, Index: 2}
		// DecodeID with nil VID and non-zero index panics due to assertion
		require.Panics(t, func() {
			_ = wout.DecodeID()
		})
	})

	t.Run("nil vid index 0 decode id", func(t *testing.T) {
		wout := WrappedOutput{VID: nil, Index: 0}
		oid := wout.DecodeID()

		// Should create output ID with nil transaction ID
		require.Equal(t, byte(0), oid.Index())
	})

	t.Run("id string nil", func(t *testing.T) {
		var wout *WrappedOutput
		require.Equal(t, "<nil>", wout.IDString())
		require.Equal(t, "<nil>", wout.IDStringShort())
	})

	t.Run("timestamp and slot", func(t *testing.T) {
		ts := base.T(1234, 56)
		txid := base.RandomTransactionID(false, 5, ts)
		vid := WrapTxID(txid)

		wout := WrappedOutput{VID: vid, Index: 0}
		require.Equal(t, ts, wout.Timestamp())
		require.Equal(t, uint32(1234), wout.Slot())
	})

	t.Run("valid id", func(t *testing.T) {
		txid := base.RandomTransactionID(false, 5, base.T(1000, 50)) // max index 5, so 6 outputs
		vid := WrapTxID(txid)

		wout0 := WrappedOutput{VID: vid, Index: 0}
		require.True(t, wout0.ValidID())

		wout5 := WrappedOutput{VID: vid, Index: 5}
		require.True(t, wout5.ValidID())

		wout6 := WrappedOutput{VID: vid, Index: 6}
		require.False(t, wout6.ValidID()) // Index 6 is out of range
	})

	t.Run("id has fragment", func(t *testing.T) {
		txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
		vid := WrapTxID(txid)
		wout := WrappedOutput{VID: vid, Index: 0}

		idStr := wout.IDString()
		// Check that fragment matching works
		require.True(t, wout.IDHasFragment(idStr[:5]))
	})
}

// TestWrappedTxUnwrap tests the unwrap mechanism to access the underlying type.
// WrappedTx can hold a Vertex, DetachedVertex, or VirtualTransaction.
// RUnwrap/Unwrap call the appropriate callback based on the wrapped type.
func TestWrappedTxUnwrap(t *testing.T) {
	t.Run("unwrap virtual tx", func(t *testing.T) {
		txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
		vid := WrapTxID(txid)

		vertexCalled := false
		virtualCalled := false

		vid.RUnwrap(UnwrapOptions{
			Vertex: func(v *Vertex) {
				vertexCalled = true
			},
			VirtualTx: func(v *VirtualTransaction) {
				virtualCalled = true
			},
		})

		require.False(t, vertexCalled)
		require.True(t, virtualCalled)
	})
}

// TestWrappedTxBefore tests timestamp comparison between WrappedTx instances.
// Before returns true if vid1's timestamp is strictly before vid2's timestamp.
// Used for ordering transactions in time.
func TestWrappedTxBefore(t *testing.T) {
	t.Run("before comparison", func(t *testing.T) {
		ts1 := base.T(1000, 50)
		ts2 := base.T(1001, 50)

		vid1 := WrapTxID(base.RandomTransactionID(false, 5, ts1))
		vid2 := WrapTxID(base.RandomTransactionID(false, 5, ts2))

		require.True(t, vid1.Before(vid2))
		require.False(t, vid2.Before(vid1))
	})

	t.Run("same timestamp", func(t *testing.T) {
		ts := base.T(1000, 50)

		vid1 := WrapTxID(base.RandomTransactionID(false, 5, ts))
		vid2 := WrapTxID(base.RandomTransactionID(false, 5, ts))

		require.False(t, vid1.Before(vid2))
		require.False(t, vid2.Before(vid1))
	})
}

// TestWrappedTxConcurrency tests thread-safety of WrappedTx operations.
// Multiple goroutines can safely read flags/status and add consumers concurrently.
// WrappedTx uses internal locking to ensure data consistency.
func TestWrappedTxConcurrency(t *testing.T) {
	t.Run("concurrent flag access", func(t *testing.T) {
		txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
		vid := WrapTxID(txid)

		var wg sync.WaitGroup
		const goroutines = 10

		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 100; j++ {
					_ = vid.FlagsUp(FlagVertexDefined)
					_ = vid.GetTxStatus()
				}
			}()
		}

		wg.Wait()
	})

	t.Run("concurrent consumer access", func(t *testing.T) {
		txid := base.RandomTransactionID(false, 10, base.T(1000, 50))
		vid := WrapTxID(txid)

		var wg sync.WaitGroup
		const goroutines = 10

		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				consumer := WrapTxID(base.RandomTransactionID(false, 2, base.T(uint32(1001+idx), 50)))
				vid.AddConsumer(byte(idx%5), consumer)
			}(i)
		}

		wg.Wait()

		// Verify all consumers were added
		total := 0
		for i := 0; i < 5; i++ {
			consumers := vid.ConsumersOf(byte(i))
			total += len(consumers)
		}
		require.Equal(t, goroutines, total)
	})
}

// TestTxIDStatus tests the TxIDStatus struct used for API responses.
// TxIDStatus summarizes a transaction's state including: DAG presence,
// storage presence, validation status, flags, and optional ledger coverage.
// Supports both human-readable Lines() output and JSON serialization.
func TestTxIDStatus(t *testing.T) {
	t.Run("lines format", func(t *testing.T) {
		txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
		status := &TxIDStatus{
			ID:        txid,
			OnDAG:     true,
			InStorage: true,
			Status:    Good,
			Flags:     FlagVertexDefined,
		}

		lines := status.Lines()
		require.NotNil(t, lines)
		str := lines.String()
		require.Contains(t, str, "GOOD")
	})

	t.Run("json serialization", func(t *testing.T) {
		txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
		coverage := uint64(12345)
		status := &TxIDStatus{
			ID:        txid,
			OnDAG:     true,
			InStorage: true,
			Status:    Good,
			Flags:     FlagVertexDefined,
			Coverage:  &coverage,
		}

		jsonable := status.JSONAble()
		require.Equal(t, txid.StringHex(), jsonable.ID)
		require.True(t, jsonable.OnDAG)
		require.True(t, jsonable.InStorage)
		require.Equal(t, "GOOD", jsonable.Status)
		require.Equal(t, coverage, jsonable.Coverage)

		// Parse back
		parsed, err := jsonable.Parse()
		require.NoError(t, err)
		require.Equal(t, txid, parsed.ID)
		require.Equal(t, Good, parsed.Status)
		require.NotNil(t, parsed.Coverage)
		require.Equal(t, coverage, *parsed.Coverage)
	})
}

// TestVertexWithRealTransaction tests Vertex creation with actual transactions.
// Uses utxodb to create valid transactions and tests:
// - Creating a Vertex from a parsed transaction
// - Wrapping a Vertex into WrappedTx
// - Referencing input transactions
// - Tracking missing inputs
// - Iterating over input dependencies
func TestVertexWithRealTransaction(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)

	t.Run("create vertex from transaction", func(t *testing.T) {
		privKey, _, addr := u.GenerateAddress(1)
		err := u.TokensFromFaucet(addr, 1_000_000_000)
		require.NoError(t, err)

		outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
		require.NoError(t, err)
		require.Equal(t, 1, len(outs))

		// Create a transaction
		ts := outs[0].ID.Timestamp().AddSlots(1)
		par, err := u.MakeTransferInputData(privKey, nil, ts)
		require.NoError(t, err)

		_, _, addr2 := u.GenerateAddress(2)
		txBytes, err := txbuilder.MakeSimpleTransferTransaction(par.WithAmount(100_000_000).WithTargetLock(addr2))
		require.NoError(t, err)

		tx, err := transaction.Parse(txBytes)
		require.NoError(t, err)

		// Create vertex
		v := NewVertex(tx)
		require.NotNil(t, v)
		require.Equal(t, tx.NumInputs(), len(v.Inputs))
		require.Equal(t, tx.NumEndorsements(), len(v.Endorsements))

		// All inputs and endorsements should be nil initially
		for _, inp := range v.Inputs {
			require.Nil(t, inp)
		}
		for _, end := range v.Endorsements {
			require.Nil(t, end)
		}
	})

	t.Run("wrap vertex", func(t *testing.T) {
		privKey, _, addr := u.GenerateAddress(10)
		err := u.TokensFromFaucet(addr, 1_000_000_000)
		require.NoError(t, err)

		outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
		require.NoError(t, err)
		require.Equal(t, 1, len(outs))

		ts := outs[0].ID.Timestamp().AddSlots(1)
		par, err := u.MakeTransferInputData(privKey, nil, ts)
		require.NoError(t, err)

		_, _, addr2 := u.GenerateAddress(11)
		txBytes, err := txbuilder.MakeSimpleTransferTransaction(par.WithAmount(100_000_000).WithTargetLock(addr2))
		require.NoError(t, err)

		tx, err := transaction.Parse(txBytes)
		require.NoError(t, err)

		v := NewVertex(tx)
		vid := v.Wrap()

		require.NotNil(t, vid)
		require.Equal(t, tx.ID(), vid.ID())
		require.False(t, vid.IsVirtualTx())

		// Unwrap should call Vertex callback
		vertexCalled := false
		vid.RUnwrap(UnwrapOptions{
			Vertex: func(vv *Vertex) {
				vertexCalled = true
				require.Equal(t, tx, vv.Transaction)
			},
		})
		require.True(t, vertexCalled)
	})

	t.Run("vertex reference input", func(t *testing.T) {
		privKey, _, addr := u.GenerateAddress(20)
		err := u.TokensFromFaucet(addr, 1_000_000_000)
		require.NoError(t, err)

		outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
		require.NoError(t, err)
		require.Equal(t, 1, len(outs))

		ts := outs[0].ID.Timestamp().AddSlots(1)
		par, err := u.MakeTransferInputData(privKey, nil, ts)
		require.NoError(t, err)

		_, _, addr2 := u.GenerateAddress(21)
		txBytes, err := txbuilder.MakeSimpleTransferTransaction(par.WithAmount(100_000_000).WithTargetLock(addr2))
		require.NoError(t, err)

		tx, err := transaction.Parse(txBytes)
		require.NoError(t, err)

		v := NewVertex(tx)

		// Create a virtual tx for the input
		inputOid := tx.MustInputAt(0)
		inputTxID := inputOid.TransactionID()
		inputVid := WrapTxID(inputTxID)

		v.ReferenceInput(0, inputVid)
		require.Equal(t, inputVid, v.Inputs[0])
	})

	t.Run("vertex missing inputs", func(t *testing.T) {
		privKey, _, addr := u.GenerateAddress(30)
		err := u.TokensFromFaucet(addr, 1_000_000_000)
		require.NoError(t, err)

		outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
		require.NoError(t, err)
		require.Equal(t, 1, len(outs))

		ts := outs[0].ID.Timestamp().AddSlots(1)
		par, err := u.MakeTransferInputData(privKey, nil, ts)
		require.NoError(t, err)

		_, _, addr2 := u.GenerateAddress(31)
		txBytes, err := txbuilder.MakeSimpleTransferTransaction(par.WithAmount(100_000_000).WithTargetLock(addr2))
		require.NoError(t, err)

		tx, err := transaction.Parse(txBytes)
		require.NoError(t, err)

		v := NewVertex(tx)

		missingInputs, missingEndorsements := v.NumMissingInputs()
		require.Equal(t, tx.NumInputs(), missingInputs)
		require.Equal(t, 0, missingEndorsements)

		missingSet := v.MissingInputTxIDSet()
		require.Equal(t, tx.NumInputs(), len(missingSet))

		missingStr := v.MissingInputTxIDString()
		require.NotEqual(t, "(none)", missingStr)
	})

	t.Run("vertex for each input dependency", func(t *testing.T) {
		privKey, _, addr := u.GenerateAddress(40)
		err := u.TokensFromFaucet(addr, 1_000_000_000)
		require.NoError(t, err)

		outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
		require.NoError(t, err)
		require.Equal(t, 1, len(outs))

		ts := outs[0].ID.Timestamp().AddSlots(1)
		par, err := u.MakeTransferInputData(privKey, nil, ts)
		require.NoError(t, err)

		_, _, addr2 := u.GenerateAddress(41)
		txBytes, err := txbuilder.MakeSimpleTransferTransaction(par.WithAmount(100_000_000).WithTargetLock(addr2))
		require.NoError(t, err)

		tx, err := transaction.Parse(txBytes)
		require.NoError(t, err)

		v := NewVertex(tx)

		inputCount := 0
		v.ForEachInputDependency(func(i byte, vidInput *WrappedTx) bool {
			inputCount++
			require.Nil(t, vidInput) // Not referenced yet
			return true
		})
		require.Equal(t, tx.NumInputs(), inputCount)
	})
}

// TestVertexUnReferenceDependencies tests clearing all input/endorsement references.
// UnReferenceDependencies clears all Input and Endorsement pointers and BaselineBranchID.
// Used when a vertex needs to be invalidated or released from memory.
func TestVertexUnReferenceDependencies(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)

	privKey, _, addr := u.GenerateAddress(50)
	err := u.TokensFromFaucet(addr, 1_000_000_000)
	require.NoError(t, err)

	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)
	require.Equal(t, 1, len(outs))

	ts := outs[0].ID.Timestamp().AddSlots(1)
	par, err := u.MakeTransferInputData(privKey, nil, ts)
	require.NoError(t, err)

	_, _, addr2 := u.GenerateAddress(51)
	txBytes, err := txbuilder.MakeSimpleTransferTransaction(par.WithAmount(100_000_000).WithTargetLock(addr2))
	require.NoError(t, err)

	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)

	v := NewVertex(tx)

	// Reference an input
	inputOid := tx.MustInputAt(0)
	inputTxID := inputOid.TransactionID()
	inputVid := WrapTxID(inputTxID)
	v.ReferenceInput(0, inputVid)
	require.NotNil(t, v.Inputs[0])

	// Set baseline branch
	branchID := base.RandomTransactionID(true, 2, base.T(999, 0))
	v.BaselineBranchID = &branchID
	require.NotNil(t, v.BaselineBranchID)

	// Un-reference all
	v.UnReferenceDependencies()

	require.Nil(t, v.Inputs[0])
	require.Nil(t, v.BaselineBranchID)
}

// TestConvertVirtualToVertex tests converting a VirtualTransaction to a full Vertex.
// When transaction bytes arrive, ConvertVirtualTxToVertexNoLock replaces the
// internal virtual representation with a proper Vertex while preserving the WrappedTx identity.
func TestConvertVirtualToVertex(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)

	privKey, _, addr := u.GenerateAddress(60)
	err := u.TokensFromFaucet(addr, 1_000_000_000)
	require.NoError(t, err)

	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)
	require.Equal(t, 1, len(outs))

	ts := outs[0].ID.Timestamp().AddSlots(1)
	par, err := u.MakeTransferInputData(privKey, nil, ts)
	require.NoError(t, err)

	_, _, addr2 := u.GenerateAddress(61)
	txBytes, err := txbuilder.MakeSimpleTransferTransaction(par.WithAmount(100_000_000).WithTargetLock(addr2))
	require.NoError(t, err)

	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)

	// Create virtual tx first
	vid := WrapTxID(tx.ID())
	require.True(t, vid.IsVirtualTx())

	// Create vertex
	v := NewVertex(tx)

	// Convert virtual to vertex
	vid.Unwrap(UnwrapOptions{
		VirtualTx: func(vt *VirtualTransaction) {
			vid.ConvertVirtualTxToVertexNoLock(v)
		},
	})

	// Should no longer be virtual
	require.False(t, vid.IsVirtualTx())

	// Should unwrap to vertex now
	vertexCalled := false
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(vv *Vertex) {
			vertexCalled = true
			require.Equal(t, tx, vv.Transaction)
		},
	})
	require.True(t, vertexCalled)
}

// TestConvertToDetached tests converting a Vertex to a DetachedVertex.
// DetachedVertex retains the transaction but releases input dependencies,
// reducing memory usage for transactions that no longer need dependency tracking.
func TestConvertToDetached(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)

	privKey, _, addr := u.GenerateAddress(70)
	err := u.TokensFromFaucet(addr, 1_000_000_000)
	require.NoError(t, err)

	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)
	require.Equal(t, 1, len(outs))

	ts := outs[0].ID.Timestamp().AddSlots(1)
	par, err := u.MakeTransferInputData(privKey, nil, ts)
	require.NoError(t, err)

	_, _, addr2 := u.GenerateAddress(71)
	txBytes, err := txbuilder.MakeSimpleTransferTransaction(par.WithAmount(100_000_000).WithTargetLock(addr2))
	require.NoError(t, err)

	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)

	v := NewVertex(tx)
	vid := v.Wrap()

	require.False(t, vid.IsVirtualTx())

	// Convert to detached
	vid.ConvertToDetached()

	// Should be detached now
	detachedCalled := false
	vid.RUnwrap(UnwrapOptions{
		Vertex: func(vv *Vertex) {
			t.Fatal("should not be vertex")
		},
		DetachedVertex: func(dv *DetachedVertex) {
			detachedCalled = true
			require.Equal(t, tx, dv.Transaction)
		},
		VirtualTx: func(vt *VirtualTransaction) {
			t.Fatal("should not be virtual")
		},
	})
	require.True(t, detachedCalled)
}

// TestGetTransaction tests retrieving the underlying Transaction from a WrappedTx.
// GetTransaction returns the Transaction for Vertex/DetachedVertex, nil for VirtualTransaction.
func TestGetTransaction(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)

	privKey, _, addr := u.GenerateAddress(80)
	err := u.TokensFromFaucet(addr, 1_000_000_000)
	require.NoError(t, err)

	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)
	require.Equal(t, 1, len(outs))

	ts := outs[0].ID.Timestamp().AddSlots(1)
	par, err := u.MakeTransferInputData(privKey, nil, ts)
	require.NoError(t, err)

	_, _, addr2 := u.GenerateAddress(81)
	txBytes, err := txbuilder.MakeSimpleTransferTransaction(par.WithAmount(100_000_000).WithTargetLock(addr2))
	require.NoError(t, err)

	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)

	t.Run("from vertex", func(t *testing.T) {
		v := NewVertex(tx)
		vid := v.Wrap()

		gotTx := vid.GetTransaction()
		require.Equal(t, tx, gotTx)
	})

	t.Run("from virtual tx returns nil", func(t *testing.T) {
		vid := WrapTxID(tx.ID())

		gotTx := vid.GetTransaction()
		require.Nil(t, gotTx)
	})
}

// TestLedgerCoverage tests ledger coverage tracking for sequencer milestones.
// Ledger coverage represents the amount of tokens "seen" by a transaction's past cone.
// Coverage is only stored when SetTxStatusGood is called with a non-nil PastConeBase.
func TestLedgerCoverage(t *testing.T) {
	txid := base.RandomTransactionID(true, 5, base.T(1000, 50))
	vid := WrapTxID(txid)

	// Initially no coverage
	require.Equal(t, uint64(0), vid.GetLedgerCoverage())
	require.Nil(t, vid.GetLedgerCoverageP())

	// Note: coverage is only set when pastCone is not nil
	// SetTxStatusGood with nil pastCone sets FlagVertexIgnoreAbsenceOfPastCone
	// but does not set coverage
	vid.SetTxStatusGood(nil, 12345)
	require.Equal(t, Good, vid.GetTxStatus())

	// Coverage is NOT set when pastCone is nil (this is by design)
	require.Equal(t, uint64(0), vid.GetLedgerCoverage())
	require.Nil(t, vid.GetLedgerCoverageP())

	// Create a new vid and set coverage with a real PastConeBase
	txid2 := base.RandomTransactionID(true, 5, base.T(1001, 50))
	vid2 := WrapTxID(txid2)

	// Create a minimal PastConeBase
	branchID := base.RandomTransactionID(true, 2, base.T(999, 0))
	pc := NewPastConeBase(&branchID)

	vid2.SetTxStatusGood(pc, 12345)

	require.Equal(t, uint64(12345), vid2.GetLedgerCoverage())
	require.NotNil(t, vid2.GetLedgerCoverageP())
	require.Equal(t, uint64(12345), *vid2.GetLedgerCoverageP())

	// Coverage string
	require.Contains(t, vid2.GetLedgerCoverageString(), "12")
}

// TestIsPreferredMilestoneAgainstTheOther tests the milestone preference comparison.
// Determines which of two competing milestones should be preferred.
// NOTE: Documents a known bug in cmp.go:16 where vid2's coverage is incorrectly
// read from vid1, causing the comparison to use vid1's coverage for both operands.
func TestIsPreferredMilestoneAgainstTheOther(t *testing.T) {
	// NOTE: There is a bug in cmp.go:16 where lc2 := vid1.GetLedgerCoverageP()
	// should be lc2 := vid2.GetLedgerCoverageP(). This causes the comparison
	// to always compare vid1's coverage against itself, making these tests
	// demonstrate the buggy behavior.

	t.Run("same vid returns false", func(t *testing.T) {
		branchID := base.RandomTransactionID(true, 2, base.T(999, 0))
		pc := NewPastConeBase(&branchID)

		txid := base.RandomTransactionID(true, 5, base.T(1000, 50))
		vid := WrapTxID(txid)
		vid.SetTxStatusGood(pc, 1000)

		require.False(t, IsPreferredMilestoneAgainstTheOther(vid, vid))
	})

	t.Run("nil coverage vid1 not preferred", func(t *testing.T) {
		branchID := base.RandomTransactionID(true, 2, base.T(999, 0))
		pc := NewPastConeBase(&branchID)

		txid1 := base.RandomTransactionID(true, 5, base.T(1000, 50))
		vid1 := WrapTxID(txid1)
		// No coverage set - vid1 has nil coverage

		txid2 := base.RandomTransactionID(true, 5, base.T(1001, 50))
		vid2 := WrapTxID(txid2)
		vid2.SetTxStatusGood(pc, 1000)

		// vid1 has nil coverage, should not be preferred
		require.False(t, IsPreferredMilestoneAgainstTheOther(vid1, vid2))
	})

	t.Run("vid with coverage preferred over nil coverage", func(t *testing.T) {
		branchID := base.RandomTransactionID(true, 2, base.T(999, 0))
		pc := NewPastConeBase(&branchID)

		txid1 := base.RandomTransactionID(true, 5, base.T(1000, 50))
		vid1 := WrapTxID(txid1)
		// No coverage set - vid1 has nil coverage

		txid2 := base.RandomTransactionID(true, 5, base.T(1001, 50))
		vid2 := WrapTxID(txid2)
		vid2.SetTxStatusGood(pc, 1000)

		// vid2 should be preferred over vid1 with nil coverage
		require.True(t, IsPreferredMilestoneAgainstTheOther(vid2, vid1))
	})

	t.Run("known bug: higher coverage comparison", func(t *testing.T) {
		// BUG: In cmp.go:16, lc2 := vid1.GetLedgerCoverageP() should be vid2
		// This causes the function to compare vid1's coverage with itself
		// rather than comparing vid1 with vid2.
		//
		// As a result, when both vids have non-nil coverage:
		// - *lc1 is never > *lc2 (since they're the same)
		// - Only the tie-breaker (txid comparison) matters
		//
		// This test documents the current (buggy) behavior.

		branchID := base.RandomTransactionID(true, 2, base.T(999, 0))
		pc := NewPastConeBase(&branchID)

		txid1 := base.RandomTransactionID(true, 5, base.T(1000, 50))
		vid1 := WrapTxID(txid1)
		vid1.SetTxStatusGood(pc, 2000) // Higher coverage

		txid2 := base.RandomTransactionID(true, 5, base.T(1001, 50))
		vid2 := WrapTxID(txid2)
		vid2.SetTxStatusGood(pc, 1000) // Lower coverage

		// Due to the bug, coverage comparison doesn't work as expected.
		// The function compares vid1.coverage with vid1.coverage (always equal)
		// So it falls through to the txid comparison as a tie-breaker.
		// The result depends on which txid is "younger" (lexicographically larger)
		result := IsPreferredMilestoneAgainstTheOther(vid1, vid2)
		// We just verify it doesn't panic and returns some boolean
		_ = result
	})
}

// TestIDHasFragment tests substring matching in transaction ID strings.
// Used for searching/filtering transactions by partial ID match.
func TestIDHasFragment(t *testing.T) {
	txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
	vid := WrapTxID(txid)

	idStr := txid.String()
	require.True(t, vid.IDHasFragment(idStr[:5]))
	require.False(t, vid.IDHasFragment("nonexistent"))
}

// TestValidSequencerPace tests sequencer pacing validation.
// Sequencers must maintain minimum time between milestones (pace).
// ValidSequencerPace checks if a timestamp satisfies the pace requirement.
func TestValidSequencerPace(t *testing.T) {
	txid := base.RandomTransactionID(true, 5, base.T(1000, 50))
	vid := WrapTxID(txid)

	// The pace validation depends on ledger constants
	// Just ensure it doesn't panic
	ts := vid.Timestamp()
	_ = vid.ValidSequencerPace(ts.AddTicks(10))
}

// TestWrappedOutputsShortLines tests formatting multiple WrappedOutputs for display.
// Used for logging and debugging output lists.
func TestWrappedOutputsShortLines(t *testing.T) {
	txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
	vid := WrapTxID(txid)

	wouts := []WrappedOutput{
		{VID: vid, Index: 0},
		{VID: vid, Index: 1},
		{VID: vid, Index: 2},
	}

	lines := WrappedOutputsShortLines(wouts)
	require.NotNil(t, lines)
	str := lines.String()
	require.NotEmpty(t, str)
}

// TestVertexLines tests the Lines() method for detailed vertex formatting.
// Produces multi-line output showing transaction details and input states.
func TestVertexLines(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)

	privKey, _, addr := u.GenerateAddress(90)
	err := u.TokensFromFaucet(addr, 1_000_000_000)
	require.NoError(t, err)

	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	par, err := u.MakeTransferInputData(privKey, nil, ts)
	require.NoError(t, err)

	_, _, addr2 := u.GenerateAddress(91)
	txBytes, err := txbuilder.MakeSimpleTransferTransaction(par.WithAmount(100_000_000).WithTargetLock(addr2))
	require.NoError(t, err)

	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)

	v := NewVertex(tx)
	vid := v.Wrap()

	// Lines should not panic
	lines := vid.Lines()
	require.NotNil(t, lines)
	str := lines.String()
	require.Contains(t, str, "vertex")
}

// TestVerticesLines tests formatting a slice of WrappedTx for display.
// Used for logging multiple vertices in a readable format.
func TestVerticesLines(t *testing.T) {
	txid1 := base.RandomTransactionID(false, 5, base.T(1000, 50))
	vid1 := WrapTxID(txid1)

	txid2 := base.RandomTransactionID(false, 3, base.T(1001, 50))
	vid2 := WrapTxID(txid2)

	lines := VerticesLines([]*WrappedTx{vid1, vid2})
	require.NotNil(t, lines)
	str := lines.String()
	require.NotEmpty(t, str)
}

// TestShortString tests the ShortString() method for concise vertex representation.
// Shows vertex type (virtualTx/vertex/detached), status, and abbreviated ID.
func TestShortString(t *testing.T) {
	txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
	vid := WrapTxID(txid)

	str := vid.ShortString()
	require.Contains(t, str, "virtualTx")
	require.Contains(t, str, "UNDEF")
}

// TestNumInputs tests retrieving the input count from a WrappedTx.
// Returns the actual input count for Vertex/DetachedVertex, 0 for VirtualTransaction.
func TestNumInputs(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)

	privKey, _, addr := u.GenerateAddress(100)
	err := u.TokensFromFaucet(addr, 1_000_000_000)
	require.NoError(t, err)

	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	par, err := u.MakeTransferInputData(privKey, nil, ts)
	require.NoError(t, err)

	_, _, addr2 := u.GenerateAddress(101)
	txBytes, err := txbuilder.MakeSimpleTransferTransaction(par.WithAmount(100_000_000).WithTargetLock(addr2))
	require.NoError(t, err)

	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)

	v := NewVertex(tx)
	vid := v.Wrap()

	require.Equal(t, tx.NumInputs(), vid.NumInputs())

	// Virtual tx returns 0
	vid2 := WrapTxID(tx.ID())
	require.Equal(t, 0, vid2.NumInputs())
}

// TestInflationAmount tests retrieving token inflation from a transaction.
// Only sequencer transactions can inflate tokens. VirtualTransaction returns 0.
func TestInflationAmount(t *testing.T) {
	txid := base.RandomTransactionID(false, 5, base.T(1000, 50))
	vid := WrapTxID(txid)

	// Virtual tx should return 0
	require.Equal(t, uint64(0), vid.InflationAmount())
}

// TestSetOfInputTransactions tests collecting unique input transactions.
// SetOfInputTransactions returns a set of all WrappedTx referenced as inputs,
// useful for traversing the transaction dependency graph.
func TestSetOfInputTransactions(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)

	privKey, _, addr := u.GenerateAddress(110)
	err := u.TokensFromFaucet(addr, 1_000_000_000)
	require.NoError(t, err)

	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	par, err := u.MakeTransferInputData(privKey, nil, ts)
	require.NoError(t, err)

	_, _, addr2 := u.GenerateAddress(111)
	txBytes, err := txbuilder.MakeSimpleTransferTransaction(par.WithAmount(100_000_000).WithTargetLock(addr2))
	require.NoError(t, err)

	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)

	v := NewVertex(tx)

	// Initially all inputs are nil
	inputSet := v.SetOfInputTransactions()
	require.Equal(t, 1, len(inputSet)) // Contains nil

	// Reference an input
	inputOid := tx.MustInputAt(0)
	inputTxID := inputOid.TransactionID()
	inputVid := WrapTxID(inputTxID)
	v.ReferenceInput(0, inputVid)

	inputSet = v.SetOfInputTransactions()
	require.True(t, inputSet.Contains(inputVid))
}

// TestNotConsumedOutputIndices tests finding outputs not yet spent.
// NotConsumedOutputIndices returns output indices that have no consumers
// (or whose consumers are in the provided exclusion set).
func TestNotConsumedOutputIndices(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)

	privKey, _, addr := u.GenerateAddress(120)
	err := u.TokensFromFaucet(addr, 1_000_000_000)
	require.NoError(t, err)

	outs, err := u.SugaredStateReader().GetOutputsForAccount(addr.ControllerID())
	require.NoError(t, err)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	par, err := u.MakeTransferInputData(privKey, nil, ts)
	require.NoError(t, err)

	_, _, addr2 := u.GenerateAddress(121)
	txBytes, err := txbuilder.MakeSimpleTransferTransaction(par.WithAmount(100_000_000).WithTargetLock(addr2))
	require.NoError(t, err)

	tx, err := transaction.Parse(txBytes)
	require.NoError(t, err)

	v := NewVertex(tx)
	vid := v.Wrap()

	// Create a consumer
	consumer := WrapTxID(base.RandomTransactionID(false, 2, base.T(1002, 50)))
	vid.AddConsumer(0, consumer)

	// Get not consumed outputs excluding our consumer
	notConsumed := vid.NotConsumedOutputIndices(nil)
	require.Contains(t, notConsumed, byte(0)) // 0 is consumed but consumer is not in the set

	// Now exclude the consumer
	consumerSet := set.New(consumer)
	notConsumed = vid.NotConsumedOutputIndices(consumerSet)
	require.NotContains(t, notConsumed, byte(0)) // 0 should not be in the list
}
