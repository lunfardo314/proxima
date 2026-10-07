//! The three calls of the seeker protocol (kb/external_nonce_seeker.md),
//! over a blocking HTTP client. The seeker is the client: it dials the miner.

use serde::{Deserialize, Serialize};
use std::time::Duration;

#[derive(Deserialize, Debug, Default)]
pub struct JobWire {
    pub id: u64,
    #[serde(default)]
    pub alpha_prefix: String,
    #[serde(default)]
    pub k: u32,
    #[serde(default)]
    pub beat: String,
    #[serde(default)]
    pub ttl_ms: i64,
    #[serde(default)]
    pub pubkey: String,
    #[serde(default)]
    pub slot: u64,
}

#[derive(Serialize)]
struct ResultWire<'a> {
    id: u64,
    nonce: String,
    beta: String,
    attempts: u64,
    seeker: &'a str,
}

#[derive(Serialize)]
struct ReportWire<'a> {
    id: u64,
    attempts: u64,
    seeker: &'a str,
}

#[derive(Deserialize)]
struct ErrorWire {
    #[serde(default)]
    error: String,
}

/// What the miner said to a posted result.
pub enum Verdict {
    Accepted,
    /// 409: the job is over, nothing to fix.
    Stale,
    /// 422: the nonce does not solve the job as the miner sees it, a seeker bug.
    Rejected(String),
}

pub struct Client {
    agent: ureq::Agent,
    base: String,
    token: Option<String>,
}

impl Client {
    pub fn new(base: &str, token: Option<String>) -> Client {
        let agent = ureq::AgentBuilder::new()
            .timeout_connect(Duration::from_secs(5))
            // a long-poll may legitimately hold the connection for the wait
            .timeout_read(Duration::from_secs(30))
            .timeout_write(Duration::from_secs(10))
            .build();
        Client { agent, base: base.trim_end_matches('/').to_string(), token }
    }

    fn with_auth(&self, req: ureq::Request) -> ureq::Request {
        match &self.token {
            Some(t) => req.set("Authorization", &format!("Bearer {t}")),
            None => req,
        }
    }

    /// Long-polls the current job: returns when it differs from `after` or
    /// after `wait_ms`, whichever is first.
    pub fn get_job(&self, after: u64, wait_ms: u64) -> Result<JobWire, String> {
        let req = self
            .agent
            .get(&format!("{}/seeker/job", self.base))
            .query("after", &after.to_string())
            .query("wait", &wait_ms.to_string());
        let resp = self.with_auth(req).call().map_err(describe)?;
        resp.into_json::<JobWire>().map_err(|e| format!("job response: {e}"))
    }

    pub fn post_result(&self, id: u64, nonce: u64, beta: &[u8; 64], attempts: u64, seeker: &str) -> Result<Verdict, String> {
        let body = ResultWire { id, nonce: hex::encode(nonce.to_be_bytes()), beta: hex::encode(beta), attempts, seeker };
        let req = self.with_auth(self.agent.post(&format!("{}/seeker/result", self.base)));
        match req.send_json(&body) {
            Ok(_) => Ok(Verdict::Accepted),
            Err(ureq::Error::Status(409, _)) => Ok(Verdict::Stale),
            Err(ureq::Error::Status(422, resp)) => {
                let reason = resp.into_json::<ErrorWire>().map(|e| e.error).unwrap_or_default();
                Ok(Verdict::Rejected(reason))
            }
            Err(e) => Err(describe(e)),
        }
    }

    pub fn post_report(&self, id: u64, attempts: u64, seeker: &str) -> Result<(), String> {
        let body = ReportWire { id, attempts, seeker };
        let req = self.with_auth(self.agent.post(&format!("{}/seeker/report", self.base)));
        req.send_json(&body).map(|_| ()).map_err(describe)
    }
}

fn describe(e: ureq::Error) -> String {
    match e {
        ureq::Error::Status(code, resp) => {
            let reason = resp.into_json::<ErrorWire>().map(|e| e.error).unwrap_or_default();
            format!("HTTP {code} {reason}")
        }
        ureq::Error::Transport(t) => t.to_string(),
    }
}
