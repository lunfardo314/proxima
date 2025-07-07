package ledger

// TODO minimize use of atTuple8

const _generalFunctionsYAML = `
functions:
   -
      sym: amountConstraint
      numArgs: 1
      source: "atTuple8($0, amountsConstraintIndex)"
   -
      sym: lockConstraint
      numArgs: 1
      source: "atTuple8($0, lockConstraintIndex)"
   -
      sym: isPathToConsumedOutput
      numArgs: 1
      source: hasPrefix($0, pathToConsumedOutputs)
   -
      sym: isPathToProducedOutput
      numArgs: 1
      source: hasPrefix($0, pathToProducedOutputs)
   -
      sym: consumedOutputPathByIndex
      numArgs: 1
      source: concat(pathToConsumedOutputs,$0)
   -
      sym: unlockParamsPathByIndex
      numArgs: 1
      source: concat(pathToUnlockParams,$0)
   -
      sym: producedOutputPathByIndex
      numArgs: 1
      source: concat(pathToProducedOutputs,$0)
   -
      sym: consumedOutputByIndex
      numArgs: 1
      source: "atPath(consumedOutputPathByIndex($0))"
   -
      sym: unlockParamsByIndex
      numArgs: 1
      source: "atPath(unlockParamsPathByIndex($0))"
   -
      sym: producedOutputByIndex
      numArgs: 1
      source: "atPath(producedOutputPathByIndex($0))"
   -
      sym: producedConstraintByIndex
      numArgs: 1
      source: "atTuple8(producedOutputByIndex(byte($0,0)), byte($0,1))"
   -
      sym: consumedConstraintByIndex
      numArgs: 1
      source: "atPath(concat(pathToConsumedOutputs,$0,$1))"
   -
      sym: unlockParamsByConstraintIndex
      numArgs: 1
      source: "atTuple8(unlockParamsByIndex(byte($0,0)), byte($0,1))"
   -
      sym: inputIDByIndex
      numArgs: 1
      source: "atPath(concat(pathToInputIDs,$0))"
   -
      sym: timestampOfInputByIndex
      numArgs: 1
      source: timestampBytesFromPrefix(inputIDByIndex($0))
   -
      sym: timeSlotOfInputByIndex
      numArgs: 1
      source: first4Bytes(inputIDByIndex($0))
   -
      sym: txBytes
      numArgs: 0
      source: "atPath(pathToTransaction)"
   -
      sym: txSignature
      numArgs: 0
      source: "atPath(pathToSignature)"
   -
      sym: txTimestampBytes
      numArgs: 0
      source: "atPath(pathToTimestamp)"
   -
      sym: txExplicitBaseline
      numArgs: 0
      source: "atPath(pathToExplicitBaseline)"
   -
      sym: txTotalProducedAmount
      numArgs: 0
      source: "uint8Bytes(atPath(pathToTotalProducedAmount))"
   -
      sym: txSlot
      numArgs: 0
      source: first4Bytes(txTimestampBytes)
   -
      sym: txTick
      numArgs: 0
      source: tickFromTimestampBytes(txTimestampBytes)
   -
      sym: txSequencerOutputIndex
      numArgs: 0
      source: "byte(atPath(pathToSeqAndStemOutputIndices), 0)"
   -
      sym: txStemOutputIndex
      numArgs: 0
      source: "byte(atPath(pathToSeqAndStemOutputIndices), 1)"
   -
      sym: sequencerFlagON
      numArgs: 1
      source: not(isZero(bitwiseAND(byte($0,4),0x01)))
   -
      sym: isSequencerTransaction
      numArgs: 0
      source: not(equal(txSequencerOutputIndex, 0xff))
   -
      sym: isBranchTransaction
      numArgs: 0
      source: and(isSequencerTransaction, not(equal(txStemOutputIndex, 0xff)))
   -
      sym: numEndorsements
      numArgs: 0
      source: "tupleLen(atPath(pathToEndorsements))"
   -
      sym: numInputs
      numArgs: 0
      source: "tupleLen(atPath(pathToInputIDs))"
   -
      sym: numProducedOutputs
      numArgs: 0
      source: "tupleLen(atPath(pathToProducedOutputs))"
   -
      sym: txEssenceBytes
      numArgs: 0
      source: "
         concat(
            atPath(pathToInputIDs), 
            atPath(pathToUnlockParams),
            atPath(pathToProducedOutputs), 
            atPath(pathToSeqAndStemOutputIndices),
            atPath(pathToTimestamp),
            atPath(pathToTotalProducedAmount),
            atPath(pathToInputCommitment), 
            atPath(pathToEndorsements),
			atPath(pathToExplicitBaseline),
            atPath(pathToLocalLibraries)
         )"
   -
      sym: txIDPrefix
      numArgs: 0
      source: if(isSequencerTransaction, bitwiseOR(txTimestampBytes, 0x0000000001), txTimestampBytes)
   -
      sym: txID
      numArgs: 0
      source: concat(txIDPrefix, byte(sub(numProducedOutputs,1), 7), slice(blake2b(txEssenceBytes),6,31))
   -
      sym: selfOutputPath
      numArgs: 0
      source: "slice(at,0,2)"
   -
      sym: selfSiblingConstraint
      numArgs: 1
      source: "atPath(concat(selfOutputPath, $0))"
   -
      sym: selfOutputBytes
      numArgs: 0
      source: "atPath(selfOutputPath)"
   -
      sym: selfNumConstraints
      numArgs: 0
      source: tupleLen(selfOutputBytes)
   -
      sym: self
      numArgs: 0
      source: "atPath(at)"
   -
      sym: selfBytecodePrefix
      numArgs: 0
      source: parsePrefixBytecode(self)
   -
      sym: selfIsConsumedOutput
      numArgs: 0
      source: "isPathToConsumedOutput(at)"
   -
      sym: selfIsProducedOutput
      numArgs: 0
      source: "isPathToProducedOutput(at)"
   -
      sym: selfOutputIndex
      numArgs: 0
      source: "byte(at, 2)"
   -
      sym: selfBlockIndex
      numArgs: 0
      source: "tail(at, 3)"
   -
      sym: selfConstraintIndex
      numArgs: 0
      source: "slice(at, 2, 3)"
   -
      sym: selfUnlockParameters
      numArgs: 0
      source: "atPath(concat(pathToUnlockParams, selfConstraintIndex))"
   -
      sym: selfSiblingUnlockBlock
      numArgs: 1
      source: "atTuple8(atPath(concat(pathToUnlockParams, selfOutputIndex)), $0)"
   -
      sym: selfHashUnlock
      numArgs: 1
      source: if(equal($0, blake2b(selfUnlockParameters)),selfUnlockParameters,nil)
   -
      sym: signatureED25519
      numArgs: 1
      source: slice($0, 0, 63)
   -
      sym: publicKeyED25519
      numArgs: 1
      source: slice($0, 64, 95)
`
