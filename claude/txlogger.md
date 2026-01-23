# Transaction logger (txlogger)

## Purpose
Transaction logger is an implementation if the interfaces `global.TxLogger` and `global.TxWriter`. 
It is a facility which allows:
- to record log messages about a specific transactions with timestamps in a key/value storage.
- to retrieve records attributed to a specific transaction based on its ID or part of it

The `txlogger` can be globally enabled/disabled to the necessary log detail level. 

## Timestamp
it is big endian value of the time.Time (8 bytes of nanoseconds) of the moment of writing the message. 
Each message has unique timestamp 

## Implementation of the txlogger storage 
It implements `global.TxLogger` interface based on the `global.Store` interface.

The log message for the txid is stored as a batch of several key-value pairs (|| is concatenation):
- key=txid.TransactionHash || timestamp, value = first 6 bytes of the txid || len(msg) || msg 
- key=timestamp || txid.TransactionHash

This allows writing of the messages, retrieving it by the txid and traversing in the timestamp interval 