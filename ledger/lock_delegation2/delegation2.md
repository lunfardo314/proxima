## Delegation 2

The delegation version 1 has significant drawbacks
- it is not very scalable. 1 mil delegations, moving them in transactions every slot would generate approx 1000000/250/10 = 400 TPS of very big (~20K) size transactions
- if we move tose delegations not every slot, but say 1 out of N, we will lose ledger coverage accordingly. E.g. N = every 3rd slot means we lose 67% of the ledger coverage of the delegated capital

Principles how Delegation 2 solves the problem:

- constant _delegation epoch length_ $E$. may be some 350 slots (~1 hour).  
- some part of the delegations epoch only owner of the delegated output will be able to consume the output. That will be _safe revocation window_
- to revoke delegation from sequencer, the _trusted revocation protocol_ will be used. _Trusted revocation protocol_ is a safe command to the sequencer to mark delegation output "terminated delegation". It is up to the sequencer to obey to the command or not. Normally it will obey due to reputation reasons
- if sequencer does not perform command as supposed, safe revocation protocol takes effect: owner will wait until safe revocation window and will consume the output herself. 
- the same will happen if target sequencer is down: nobody will prevent owner from consuming her output 

Reasonable length of the safe revocation window would be say 20 slots (~3.4 minutes). Every hour, ~3.4 (~5.6% of total time) sequencer will not be able to consume the delegation output, therefore it will be enough time for the user to consume that output and revoke delegation

On the sequencer side, at the beginning of the delegation epoch, sequencer will:
- lock delegation output for the period of delegation epoch
- accrue _proven coverage_ for the locked period in the sequencer output. 
- _proven coverage_ value will be used for the calculation of the coverage. It is safe to use that value as the coverage provided by the sequencer because it is guaranteed that nobody else can double-use the delegation output during the lock-in period

Draft implementation:

* delegation lock: `delegate(owner, target, lock-in-epochs, index, locked-until-slot)`. Here 
  * `owner` owner lock
  * `target` delegation target
  * `lock-in-epochs` is number of epoch before safe revocation window appears. 0 means it can be locked by sequencer only for the current delegation epoch
  * `locked-until-slot` 0 means current slot only. > 0 means number of the slot until which (inclusively) the output is locked. This is muted by sequencer. The slot number cannot jump over `lock-in-epochs`   
* `sequencer` constraint contains fields `locked-coverage0`, `locked-coverage1`, `locked-coverage2`, `locked-coverage3` where `locked-coveragei` is locked coverage for the current + i epoch.  
  * sequencer tries to consume and transit each delegate output it can (not-unlocked) once per slot.
  * sequencer locks consumed delegation output as long as it can and accrues locked amounts into its corresponding fields. 
  * when sequencer output moves to the next epoch, accrued amounts are shifted `locked-coverage(i-1)` <- `locked-coveragei`
  
TBD