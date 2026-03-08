# DAG visualizer

## Background 
Proxima's cooperative consensus runs on a transaction DAG. Many selfish and profit-seeking actors are adding their transactions
to the shared global DAG in a distributed system.

The transactions are subject to the globally accepted validity rules (constraints). 
The consensus and system security depends on the behavior of permissionless actors.
It is difficult model behavior with theoretical means. Intuition based on visual observation of the DAG dynamics
is crucial for understanding the system and debugging it. 

Visual representation is also crucial for explaining and learning Proxima's vision and architecture.

Proxima already have necessary API for DAG visualization and also has DAG visualizer implemented.

## Goals
Implement browser-based DAG visualizer that displays DAG from the perspective of a node in its dynamic. 

The implementation will be a second implementation on the existing API. The goal is seek for different, more informative, intuitive and user-friendly ways 
to visualize essential features of the cooperative consensus. 

Hands-on experience on Claude as a complex front-end development tool. 

## Requirements

### API
The server API to be used is in the `api/streaming`. 

### Tools and dependencies
The user, creator of the Proxima concept and codebase, is not and expert in the front-end development and tools.

Claude should interactively propose and approach and the plan: the best architecture, language, dependencies (frameworks), other tools for the project.

This project is pretty isolated from the core development of Proxima, so it may be placed in a separate repository.
This decision must be taken interactively.  

Simplicity of codebase and the visualization system maintenance (do we need separate Web server, or it can be served from the node)  

### Vision

The visualization pane is a square. Horizontally the square is divided into _slots_. 
1 slot corresponds to 10.24 seconds (a constant, but can be taken from ledger definitions)
Normally, the width of the pane is from 1 to several minutes (6, 12, 18, ... slots + edges).

Horizontal axis is time axis, increasing to the right. Slot edges are vertical lines, light color. 
The position of the grid is linked to the local clock of the browser.
The canvas moves together with the clock, keeping current clock moment at 5 or so seconds (1/2 slot) from the right edge of the pane.
So, the whole canvas smoothly moves all width of the pane in 1, 2 or so minutes. 
Slot edges are vertical lines of light color. They move together with the canvas.

When transaction (DAG vertex) is received from the server, it is immediately placed on the canvas according to its timestamp.
It starts moving together with the canvas. Note that transaction may be "from the future" with respect to the local clock and the position of teh canvas.
It still is pinned to the canvas even if outside of the visible part of it.

The vertical axis of the pane/canvas has semantic adjusted to the identity of the transaction, the most intuitive way for the user.

Each sequencer must be assigned a fixed vertical positions preferably not overlapping, and all sequencer vertices will be placed around it, not precisely on exactly the same line.
One way to do it: sequencer transactions are vertically placed according to its sequencer ID. Vertical coordinate may be
based on C = (seqID mod N) + (64-T)/12, where N is number of active sequencers at the moment and T is `ticks` part of the timestamp.
Then C must be scaled to the real height of the pane.

Branches and non branches must have different shapes.
Each sequencer is assigned a fixed color, e.g. based on C on the rainbow scale.

Non-sequencer transactions must be gray.

Three types of DAG edges, all pointing to the left:
- consumption (between any transactions)
- endorsement (between sequencer transactions, within one slot)
- stem links (between branch transactions, on slot edges)

If transaction teh link is pointing to is not on the visualizer, the edge is not displayed

Once placed on the canvas, transaction/vartex never moves and never changes color.