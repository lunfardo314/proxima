# TODO list

- branch cutter too slow. Cutting branch one-by-one in a slot means after big sync number of cached vertices remain the same 
  - temporary workaround: restart the node
- pruner and branch cutter is disabled during sync. It means big amount of unnecessary vertices piles up and it becomes slower and slower
    - temporary workaround: restart the node. Disable any sequencers until DB is synced 
- metrics subsystem
- forking when sum of active sequencers is < 50%. 
  - possible solution: redesign inflation constraints
