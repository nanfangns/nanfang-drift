use hmac::{Hmac, Mac};
use rand::Rng;
use sha2::Sha256;
use std::ffi::{CStr, CString};
use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::os::raw::c_char;
use std::sync::{Arc, Mutex};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

type HmacSha256 = Hmac<Sha256>;

#[derive(Clone, Debug, serde::Deserialize)]
pub struct AeroNode {
    pub node_id: i32,
    pub name: String,
    pub server: String,
    pub server_port: u16,
    pub password: String,
    #[serde(rename = "aero_v2_edge_psk")]
    pub edge_psk: String,
    #[serde(rename = "aero_v2_aead_key")]
    pub aead_key: String,
}

fn sha256_prefix16(s: &str) -> Vec<u8> {
    use sha2::{Digest, Sha256};
    let hash = Sha256::digest(s.as_bytes());
    hash[..16].to_vec()
}

fn hex_decode(s: &str) -> Vec<u8> {
    (0..s.len())
        .step_by(2)
        .map(|i| u8::from_str_radix(&s[i..i + 2], 16).unwrap_or(0))
        .collect()
}

fn build_ac2_blob(node: &AeroNode, rand12: &[u8], ts: i64) -> Vec<u8> {
    let prefix = sha256_prefix16(&node.password);
    let mut header = Vec::with_capacity(43);
    header.extend_from_slice(b"AC2\x00\x02");
    header.push((node.node_id >> 8) as u8);
    header.push(node.node_id as u8);
    header.extend_from_slice(&prefix);
    header.extend_from_slice(&ts.to_be_bytes());
    header.extend_from_slice(rand12);

    let edge_psk = hex_decode(&node.edge_psk);
    let mut mac = HmacSha256::new_from_slice(&edge_psk).unwrap();
    mac.update(&header);
    let auth = mac.finalize().into_bytes()[..16].to_vec();

    let mut blob = header;
    blob.extend_from_slice(&auth);
    blob
}

fn build_client_hello(node: &AeroNode) -> Vec<u8> {
    let mut rng = rand::thread_rng();
    let tls_random: Vec<u8> = (0..32).map(|_| rng.gen()).collect();
    let session_id: Vec<u8> = (0..32).map(|_| rng.gen()).collect();
    let rand12: Vec<u8> = (0..12).map(|_| rng.gen()).collect();
    let ts = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_secs() as i64;

    let ac2_blob = build_ac2_blob(node, &rand12, ts);

    // Extensions: supported_groups + AC2
    let mut extensions = Vec::new();
    extensions.extend_from_slice(&[0x00, 0x0a, 0x00, 0x04, 0x00, 0x02, 0x00, 0x1d]);
    extensions.extend_from_slice(&[0xff, 0xa5]);
    extensions.extend_from_slice(&(ac2_blob.len() as u16).to_be_bytes());
    extensions.extend_from_slice(&ac2_blob);

    // Body
    let mut body = Vec::with_capacity(512);
    body.extend_from_slice(&[0x03, 0x03]); // TLS 1.2
    body.extend_from_slice(&tls_random);
    body.push(session_id.len() as u8);
    body.extend_from_slice(&session_id);
    // Cipher suites with length prefix
    body.extend_from_slice(&[0x00, 0x04, 0x13, 0x01, 0x13, 0x03]);
    body.extend_from_slice(&[0x01, 0x00]); // compression
    body.extend_from_slice(&(extensions.len() as u16).to_be_bytes());
    body.extend_from_slice(&extensions);

    // Handshake
    let hs_len = body.len();
    let mut hs = Vec::with_capacity(4 + hs_len);
    hs.push(1); // ClientHello
    hs.push((hs_len >> 16) as u8);
    hs.push((hs_len >> 8) as u8);
    hs.push(hs_len as u8);
    hs.extend_from_slice(&body);

    // Record
    let mut record = Vec::with_capacity(5 + hs.len());
    record.push(0x16); // Handshake
    record.extend_from_slice(&[0x03, 0x03]); // TLS 1.2
    record.extend_from_slice(&(hs.len() as u16).to_be_bytes());
    record.extend_from_slice(&hs);
    record
}

fn build_at2_frame(node: &AeroNode, host: &str, port: u16, payload: &[u8]) -> Vec<u8> {
    let prefix = sha256_prefix16(&node.password);
    let host_bytes = host.as_bytes();

    let mut body = Vec::with_capacity(24 + host_bytes.len() + payload.len());
    body.extend_from_slice(b"AT2\x00\x02");
    body.extend_from_slice(&prefix);
    body.extend_from_slice(&(host_bytes.len() as u16).to_be_bytes());
    body.extend_from_slice(host_bytes);
    body.extend_from_slice(&port.to_be_bytes());
    body.extend_from_slice(&(payload.len() as u16).to_be_bytes());
    body.extend_from_slice(payload);

    let mut record = Vec::with_capacity(5 + body.len());
    record.push(0x17); // ApplicationData
    record.extend_from_slice(&[0x03, 0x03]);
    record.extend_from_slice(&(body.len() as u16).to_be_bytes());
    record.extend_from_slice(&body);
    record
}

fn recv_exact(r: &mut dyn Read, n: usize) -> std::io::Result<Vec<u8>> {
    let mut buf = vec![0u8; n];
    r.read_exact(&mut buf)?;
    Ok(buf)
}

fn recv_tls_record(r: &mut dyn Read) -> std::io::Result<Vec<u8>> {
    let hdr = recv_exact(r, 5)?;
    let length = u16::from_be_bytes([hdr[3], hdr[4]]) as usize;
    let body = recv_exact(r, length)?;
    let mut rec = hdr;
    rec.extend_from_slice(&body);
    Ok(rec)
}

fn open_aero_tunnel(node: &AeroNode, host: &str, port: u16) -> std::io::Result<TcpStream> {
    let addr = format!("{}:{}", node.server, node.server_port);
    let mut conn = TcpStream::connect(&addr)?;
    conn.set_read_timeout(Some(Duration::from_secs(10)))?;
    conn.set_write_timeout(Some(Duration::from_secs(10)))?;

    let hello = build_client_hello(node);
    conn.write_all(&hello)?;

    // Receive ServerHello
    recv_tls_record(&mut conn)?;

    // Send AT2 frame
    let at2 = build_at2_frame(node, host, port, &[]);
    conn.write_all(&at2)?;

    // Clear timeouts
    conn.set_read_timeout(None)?;
    conn.set_write_timeout(None)?;

    Ok(conn)
}

// SOCKS5 proxy

fn socks5_auth(conn: &mut TcpStream) -> bool {
    let mut buf = [0u8; 2];
    if conn.read_exact(&mut buf).is_err() || buf[0] != 5 {
        return false;
    }
    let nmethods = buf[1] as usize;
    let mut methods = vec![0u8; nmethods];
    if conn.read_exact(&mut methods).is_err() {
        return false;
    }
    let _ = conn.write(&[5, 0]); // No auth
    true
}

fn socks5_connect(conn: &mut TcpStream) -> Option<(String, u16)> {
    let mut header = [0u8; 4];
    if conn.read_exact(&mut header).is_err() || header[0] != 5 || header[1] != 1 {
        let _ = conn.write(&[5, 7, 0, 1, 0, 0, 0, 0, 0, 0]);
        return None;
    }

    let host = match header[3] {
        1 => {
            let mut addr = [0u8; 4];
            conn.read_exact(&mut addr).ok()?;
            format!("{}.{}.{}.{}", addr[0], addr[1], addr[2], addr[3])
        }
        3 => {
            let mut alen = [0u8; 1];
            conn.read_exact(&mut alen).ok()?;
            let mut domain = vec![0u8; alen[0] as usize];
            conn.read_exact(&mut domain).ok()?;
            String::from_utf8(domain).ok()?
        }
        4 => {
            let mut addr = [0u8; 16];
            conn.read_exact(&mut addr).ok()?;
            let ip = std::net::Ipv6Addr::from(addr);
            format!("[{}]", ip)
        }
        _ => {
            let _ = conn.write(&[5, 8, 0, 1, 0, 0, 0, 0, 0, 0]);
            return None;
        }
    };

    let mut pbuf = [0u8; 2];
    conn.read_exact(&mut pbuf).ok()?;
    let port = u16::from_be_bytes(pbuf);
    Some((host, port))
}

fn relay(a: &mut TcpStream, b: &mut TcpStream) {
    use std::thread;
    let mut a_clone = a.try_clone().unwrap();
    let mut b_clone = b.try_clone().unwrap();

    let t1 = thread::spawn(move || {
        let mut buf = [0u8; 8192];
        loop {
            match a_clone.read(&mut buf) {
                Ok(0) => break,
                Ok(n) => {
                    if b_clone.write_all(&buf[..n]).is_err() {
                        break;
                    }
                }
                Err(_) => break,
            }
        }
        let _ = b_clone.shutdown(std::net::Shutdown::Write);
    });

    let mut buf = [0u8; 8192];
    loop {
        match b.read(&mut buf) {
            Ok(0) => break,
            Ok(n) => {
                if a.write_all(&buf[..n]).is_err() {
                    break;
                }
            }
            Err(_) => break,
        }
    }
    let _ = a.shutdown(std::net::Shutdown::Write);
    let _ = t1.join();
}

// FFI interface

static mut NODES: Option<Vec<AeroNode>> = None;
static mut LISTENING: bool = false;

#[no_mangle]
pub extern "C" fn nanfang_fetch_nodes(url: *const c_char) -> *mut c_char {
    let url_str = unsafe {
        if url.is_null() {
            return CString::new("error: null url").unwrap().into_raw();
        }
        CStr::from_ptr(url).to_string_lossy().into_owned()
    };

    // Fetch subscription using tokio runtime
    let rt = tokio::runtime::Runtime::new().unwrap();
    let result = rt.block_on(async {
        fetch_subscription(&url_str).await
    });

    match result {
        Ok(nodes) => {
            let count = nodes.len();
            unsafe {
                NODES = Some(nodes);
            }
            CString::new(format!("ok: {} nodes", count)).unwrap().into_raw()
        }
        Err(e) => CString::new(format!("error: {}", e)).unwrap().into_raw(),
    }
}

async fn fetch_subscription(url: &str) -> Result<Vec<AeroNode>, String> {
    let client = reqwest::Client::builder()
        .danger_accept_invalid_certs(true)
        .timeout(Duration::from_secs(15))
        .build()
        .map_err(|e| e.to_string())?;

    let resp = client.get(url).send().await.map_err(|e| e.to_string())?;
    let body = resp.text().await.map_err(|e| e.to_string())?;

    let raw_nodes: Vec<serde_json::Value> =
        serde_json::from_str(&body).map_err(|e| e.to_string())?;

    let nodes: Vec<AeroNode> = raw_nodes
        .iter()
        .filter(|n| n["type"] == "aero_v2")
        .filter_map(|n| {
            Some(AeroNode {
                node_id: n["node_id"].as_i64()? as i32,
                name: n["name"].as_str()?.to_string(),
                server: n["server"].as_str()?.to_string(),
                server_port: n["server_port"].as_u64()? as u16,
                password: n["password"].as_str()?.to_string(),
                edge_psk: n["aero_v2_edge_psk"].as_str()?.to_string(),
                aead_key: n["aero_v2_aead_key"].as_str()?.to_string(),
            })
        })
        .collect();

    Ok(nodes)
}

#[no_mangle]
pub extern "C" fn nanfang_get_node_count() -> i32 {
    unsafe {
        match &NODES {
            Some(nodes) => nodes.len() as i32,
            None => 0,
        }
    }
}

#[no_mangle]
pub extern "C" fn nanfang_get_node_name(index: i32) -> *mut c_char {
    unsafe {
        match &NODES {
            Some(nodes) => {
                if index >= 0 && (index as usize) < nodes.len() {
                    CString::new(nodes[index as usize].name.clone())
                        .unwrap()
                        .into_raw()
                } else {
                    CString::new("").unwrap().into_raw()
                }
            }
            None => CString::new("").unwrap().into_raw(),
        }
    }
}

#[no_mangle]
pub extern "C" fn nanfang_get_node_id(index: i32) -> i32 {
    unsafe {
        match &NODES {
            Some(nodes) => {
                if index >= 0 && (index as usize) < nodes.len() {
                    nodes[index as usize].node_id
                } else {
                    -1
                }
            }
            None => -1,
        }
    }
}

#[no_mangle]
pub extern "C" fn nanfang_start_proxy(listen_port: u16, node_index: i32) -> *mut c_char {
    let nodes = unsafe {
        match &NODES {
            Some(n) => n.clone(),
            None => return CString::new("error: no nodes loaded").unwrap().into_raw(),
        }
    };

    let node_index = node_index as usize;
    if node_index >= nodes.len() {
        return CString::new("error: invalid node index").unwrap().into_raw();
    }

    let node = nodes[node_index].clone();
    let addr = format!("127.0.0.1:{}", listen_port);

    std::thread::spawn(move || {
        let listener = match TcpListener::bind(&addr) {
            Ok(l) => l,
            Err(e) => {
                eprintln!("Failed to bind: {}", e);
                return;
            }
        };

        unsafe { LISTENING = true };
        eprintln!("SOCKS5 listening on {}", addr);

        for stream in listener.incoming() {
            if let Ok(mut stream) = stream {
                let node = node.clone();
                std::thread::spawn(move || {
                    if !socks5_auth(&mut stream) {
                        return;
                    }
                    if let Some((host, port)) = socks5_connect(&mut stream) {
                        match open_aero_tunnel(&node, &host, port) {
                            Ok(mut remote) => {
                                let _ = stream.write(&[5, 0, 0, 1, 0, 0, 0, 0, 0, 0]);
                                relay(&mut stream, &mut remote);
                            }
                            Err(e) => {
                                eprintln!("tunnel error {}: {}", host, e);
                                let _ = stream.write(&[5, 1, 0, 1, 0, 0, 0, 0, 0, 0]);
                            }
                        }
                    }
                });
            }
        }
    });

    CString::new(format!("ok: listening on {}", addr)).unwrap().into_raw()
}

#[no_mangle]
pub extern "C" fn nanfang_free_string(s: *mut c_char) {
    if !s.is_null() {
        unsafe {
            let _ = CString::from_raw(s);
        }
    }
}
