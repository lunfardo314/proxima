# DAG visualizer

## Background 
Proxima's cooperative consensus runs on a transaction DAG. Many selfish and profit-seeking actors are adding their transactions
to the shared global DAG in a distributed system.

The transactions are subject to the globally accepted validity rules (constraints), but the actors are free to choose their strategy. 
The consensus and system security depends on the behavior of permissionless actors.
It is difficult to model the behavior with theoretical means. Intuition based on visual observation of the DAG dynamics
is crucial for understanding the system and debugging it. 
It is also crucial for explaining and learning Proxima's vision and architecture.

Proxima already have necessary API for DAG visualization and also has DAG visualizer implemented.

## Goals
Implement browser-based DAG visualizer that displays DAG from the perspective of a node in its dynamic. 

The implementation will be a second implementation on the existing API. The goal is seek for different, more informative, intuitive and user-friendly ways 
to visualize essential features of the cooperative consensus. 

To have hands-on experience on Claude as a complex front-end development tool.
The user, creator of the Proxima concept and core codebase, is not and expert in the front-end development, languages and tools.

## Requirements

### API
The server API to be used is in the `api/streaming`. 

### Tools and dependencies


Claude should interactively propose an approach with the plan: the best architecture, language, dependencies (frameworks), other tools for the project.

This project is pretty isolated from the core development of Proxima, so it may be placed in a separate repository.
This decision must be taken interactively.  

Simplicity of codebase and the visualization system maintenance (do we need separate Web server, or it can be served from the node)  

### Vision

The visualization canvas a virtual tape, that starts at genesis and is potentially endless. 
Vertically the square is divided into _slots_. 1 slot corresponds to 10.24 seconds (a constant, but can be taken from ledger definitions)

The visualization pane is a square window to the fragment of the canvas. 
Normally, the width of the pane is from 1 to several minutes (6, 12, 18, ... slots + edges).
The window is moving along the canvas together with the clock of the browser.

Horizontal axis is a time axis It is increasing to the right. Slot edges are vertical lines with light (gray?) color. 
The position of the grid is linked to the local clock of the browser.

The canvas smoothly moves together with the clock within the visualization window (pane), keeping current clock moment at 5 or so seconds (1/2 slot) from the right edge of the pane.

Each transaction (DAG vertex) received from the server, is immediately placed on the canvas according to its timestamp.
It starts moving together with the canvas withing the pane. 
Note that transaction may be "from the future" or "from the past" with respect to the local clock and the position of the pane on the canvas.

The vertical axis of the pane/canvas must have semantic adjusted to the identity of the transaction, the most intuitive way for the user.

Each sequencer must be assigned a fixed vertical positions preferably not overlapping with others.
All vertices of the sequencer will be placed along it on the pane, not exactly on the same line.
One way to do it: sequencer transactions are vertically placed according to its sequencer ID. Vertical coordinate may be
based on C = (seqID mod N) + (64-T)/12, where N is number of active sequencers at the moment and T is `ticks` part of the timestamp.
Then C must be scaled to the real height of the pane.

Branches and non-branches must have different shapes. 
It's size can be proportional to the log(A), where A is total produced amount of the transaction 
Each sequencer is assigned a fixed color, e.g. based on C scaled to the color on teh rainbow.

Non-sequencer transactions must be gray.

Three types of DAG edges, all pointing to the left:
- consumption (between any transactions)
- endorsement (between sequencer transactions, within one slot)
- stem links (between branch transactions, on slot edges)

If transaction the link is pointing to is not on the visualizer, the edge is not displayed

Once placed on the canvas, transaction/vartex never moves and never changes color.

