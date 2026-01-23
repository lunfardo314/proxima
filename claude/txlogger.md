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

## txlogger storage 
It implements `global.TxLogger` interface based on the `global.Store` interface.

The log message for the txid is stored as a batch of several key-value pairs (|| is concatenation):
- key=txid.TransactionHash || timestamp, value = first 6 bytes of the txid || len(msg) || msg 
- key=timestamp || txid.TransactionHash

This allows writing the log messages, retrieving it by the txid and traversing in the timestamp interval

Enabling via the `TxLogEnable(lvl TxLogLevel)` means opening the DB (if needed) and creating it (if absent) as `proximadb.txlog` next to the state DB and txstore.

Enabling with options other than _all transactions_ means logging only part of `TxLog()` messages and ignoring the rest.

Disabling with `TxlogEnable(TxLogLevelOff)` means closing the DB. Logging the messages will mean no-op. The retrieval calls should return error message "tx log disabled".

In this implementation real storage behind must be Badger DB, however it must be abstracted from the rest of the implementation behind global.Store

## Queued txlog writer
The `global.TxLogWriter` interface must be implemented as a globally accessible component as part of the `global.NodeGlobal` interface.

The implementation must be queued and use patterns of the `core_modules`. This will allow logging transaction-related log messages from different parts of the code

Whenever txlog writer is enabled, cleanup routine should be activated 

## API 
The API part must use `global.TxLogReader` interface to access transaction logger.
The API endpoints must implement:
- retrieval of the log records of transaction by its txid
- retrieval up to max amount of lg records in the time interval

## Config
TTL must be specified for message in the log.
Default should be: messages older than 1 hour must be deleted from the DB by the cleanup loop  the background (RepeatInBackground) 
