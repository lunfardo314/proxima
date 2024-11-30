package server

import (
	"net/http"
	"text/template"
)

func (srv *server) getDashboard(w http.ResponseWriter, r *http.Request) {
	type HtmlData struct {
		Host string
		// Message string
	}

	// http.ServeFile(w, r, "./config/dashboard.html")
	//host := strings.Replace(r.Host, "localhost", "127.0.0.1", 1)

	// Parse the template string
	tmpl := template.Must(template.New("webpage").Parse(dashboardHTML))

	// Data to pass into the template
	data := HtmlData{
		Host: r.Host,
	}

	// Execute the template
	err := tmpl.Execute(w, data)
	if err != nil {
		writeErr(w, err.Error())
	}
}

const dashboardHTML = `

<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta http-equiv="X-UA-Compatible" content="IE=edge">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Dashboard for Proxima node</title>
	<link rel="icon" href="data:,">
    <script>
        const host = '{{.Host}}'
        const pollingPeriod = 5000  // in ms
        function convertTimestamp(ts) {
            const date = new Date(ts / 1e6); // Convert nanoseconds to milliseconds
            return date.toLocaleDateString() + ' ' + date.toLocaleTimeString();
        }
        function getMapSize(map) {
            if (map) {
                Object.keys(map).length
            }
            return 0
        }
    </script></head>
    <style>
        ul {
            list-style-type: none; /* Remove bullet points */
            padding: 0; /* Remove padding */
        }
        li {
            background-color: #f0f0f0;
            margin: 10px 0; /* Add spacing between list items */
            padding: 10px; /* Add padding inside list items */
            border-radius: 2px; /* Round the corners */
            font-family: Arial, sans-serif;
        }
        li:hover {
            background-color: #dddddd; /* Change background on hover */
        }
        .info-row {
            display: flex;
            gap: 10px;

            margin: 5px 0;
            padding: 5px;
            background-color: #f8f9fa; /* Light background for separation */
            border: 1px solid #ddd;
            border-radius: 5px;            
        }
        .label {
            font-weight: bold;
            width: 150px; /* Set a width to align the values */
        }    
  </style>
  <body>
    <h1>Node Dashboard</h1>
    <div id="node-info">
        <p>Loading node info...</p>
    </div>
    <div id="sync-info">
        <p>Loading sync info...</p>
    </div>
    <div id="peers-info">
        <p>Loading peer info...</p>
    </div>

    <script>
        // Function to fetch peers info and update the UI
        async function fetchPeersInfo() {
            try {
                const response = await fetch('http://'+host+'/api/v1/peers_info');
                const data = await response.json();
                updatePeersInfo(data);
            } catch (error) {
                console.error('Error fetching peers info:', error);
            }
        }
        async function fetchNodeInfo() {
            try {
                const response = await fetch('http://'+host+'/api/v1/node_info');
                const data = await response.json();
                updateNodeInfo(data);
            } catch (error) {
                console.error('Error fetching node info:', error);
            }
        }
        async function fetchSyncInfo() {
            try {
                const response = await fetch('http://'+host+'/api/v1/sync_info');
                const data = await response.json();
                updateSyncInfo(data);
            } catch (error) {
                console.error('Error fetching sync info:', error);
            }
        }

        // Function to update the page with node info
        function updateNodeInfo(data) {
            const nodeInfoDiv = document.getElementById('node-info');
                    let htmlContent = "<h2>Node Info</h2>" +
        "<div class='info-row'><span class='label'>Node ID:</span><span>" + data.id + "</span></div>" +
        "<div class='info-row'><span class='label'>Peers static alive:</span><span>" + data.num_static_peers + "</span></div>" +
        "<div class='info-row'><span class='label'>Peers dynamic alive:</span><span>" + data.num_dynamic_alive + "</span></div>";
            nodeInfoDiv.innerHTML = htmlContent;
        }

        // Function to update the page with sync info
        function updateSyncInfo(data) {
            const syncInfoDiv = document.getElementById('sync-info');
            let htmlContent = 
                "<div class='info-row'><span class='label'>Synced:</span><span>" + data.synced +  "</span></div>" +
                "<div class='info-row'><span class='label'>Slots (lrb / current):</span><span>" + data.lrb_slot +" / " + data.current_slot +"</span></div>";
            syncInfoDiv.innerHTML = htmlContent;
        }

        // Function to update the page with peer info
        function updatePeersInfo(data) {
            const peersInfoDiv = document.getElementById('peers-info');
            
            // Sort the peers array by peer ID
            data.peers.sort((a, b) => a.id.localeCompare(b.id));

            let htmlContent = "<h2>Peers Info</h2><ul>";

            data.peers.forEach(peer => {
                htmlContent += "<li><div class='info-row'><span class='label'>Peer ID:</span><span>" + peer.id + "</span></div>" +
                    "<div class='info-row'><span class='label'>Multi Addresses:</span><span>" + peer.multiAddresses.join(', ') + "</span></div>" +
                    "<div class='info-row'><span class='label'>Is Alive:</span><span>" + peer.is_alive + "</span></div>" +
                    "<div class='info-row'><span class='label'>Is Static:</span><span>" + peer.is_static + "</span></div>" +
                    "<div class='info-row'><span class='label'>Responds to pull:</span><span>" + peer.responds_to_pull + "</span></div>" +
                    "<div class='info-row'><span class='label'>Added:</span><span>" + convertTimestamp(peer.when_added) + "</span></div>" +
                    "<div class='info-row'><span class='label'>Last HB:</span><span>" + convertTimestamp(peer.last_heartbeat_received) + "</span></div>" +
                    "<div class='info-row'><span class='label'>Clock Diff Qu:</span><span>" + peer.clock_differences_quartiles[0] + " " + 
                    peer.clock_differences_quartiles[1] + " " + 
                    peer.clock_differences_quartiles[2] + "</span></div>" +
                    "<div class='info-row'><span class='label'>HB Diff Qu:</span><span>" + peer.hb_differences_quartiles[0] + " " +
                    peer.hb_differences_quartiles[1] + " " + 
                    peer.hb_differences_quartiles[2] + "</span></div>" + 
                    "<div class='info-row'><span class='label'># Incoming Tx:</span><span>" +  peer.num_incoming_tx + "</span></div>" +
                    "<div class='info-row'><span class='label'># Incoming HB:</span><span>" +  peer.num_incoming_hb + "</span></div>" +
                    "<div class='info-row'><span class='label'># Incoming Pull:</span><span>" +  peer.num_incoming_pull + "</span></div>" +
                    "<div class='info-row'><span class='label'>Blacklist size:</span><span>" + getMapSize(peer.blacklist) + "</span></div>" +
                    "</li>";
            });

            htmlContent += "</ul>";
            peersInfoDiv.innerHTML = htmlContent;
        }        

        // Fetch info every 5 seconds
        setInterval(fetchNodeInfo, pollingPeriod);
        setInterval(fetchSyncInfo, pollingPeriod);
        setInterval(fetchPeersInfo, pollingPeriod);

        // Initial load
        fetchNodeInfo()
        fetchSyncInfo()
        fetchPeersInfo();
    </script>
</body>
</html>
`
