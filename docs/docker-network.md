## Using docker network

The docker network in tests/docker has currently 5 nodes of which 4 are running a sequencer.
One node (currently node 3) is an access node.


### Running the docker network

Before starting you can build the docker network with the shell cmd

`./build.sh`

To start the network use the command

`./run.sh`

The network can be stopped with Ctrl+C. This will stop the nodes but after starting again, the nodes will continue where they were stopped.
If you want a fresh start with complete new initialization use the command

`./stop.sh`

before starting again.


### Structure of the project

For each node there is sub directory in tests/docker which contains

- proxi.yaml: the proxi wallet file that contains the account and private key for the account

- proxima.YAML:  configuration file for the proxima node

The directory ./boot for the bootstrap node also contains

- distribution.yaml: configuration of initial distribution of funds for the node accounts.

- start.sh: shell script that is executed on each node for initializing / startup

### Configuration of the network

#### Funds distribution
The amount of initial funds for an account can be adjusted in the file ./boot/distribution,yaml in the directory.

#### Sequencer setup
The sequencers on the nodes are installed in ./boot/start.sh:

```sh
    # setup sequencers
    if [ "$NODE_NAME" == "1" ] || [ "$NODE_NAME" == "2" ] || [ "$NODE_NAME" == "4" ]; then
        echo "setup sequencer"

        ./proxima &
        sleep 5  # let process start

        echo "node init sequencer"
        ./proxi node setup_seq --finality.weak mySeq 100000000000000

        kill_proxima
        sleep 2  # let process die
    fi 

```
With this code sequencers are currently installed on node 1, 2, and 4. Node 0 always has a sequencer.

To change the sequencer setup modify the if switch.

