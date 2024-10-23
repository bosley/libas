# Voice Transcription Server

A real-time voice transcription system that processes audio files using Whisper. The application functions as both a server and client, requiring HTTPS with self-signed certificates for secure communication.

## Setup & Usage

1. Run `x_setup.sh` to install required dependencies
2. Execute the following scripts in order:
   - `x_run_server.sh`
   - `x_run_client.sh`
   - `x_ws.sh`

## Features

- Audio processing using Whisper for accurate voice-to-text transcription
- Real-time file watching system that monitors for new audio recordings
- Built-in audio player for reviewing recorded files
- Automatic FFmpeg preprocessing of audio files for optimal transcription
- WebSocket endpoint for real-time transcription updates

## Storage Structure

Recordings are organized hierarchically:
```
recordings/
└── YYYYMMDD/
└── UUID/
├── audio_files
└── transcriptions
```

# API Documentation

## WebSocket Endpoint

### `/ws/{clientID}`
- **Method:** WebSocket Connection
- **Description:** Establishes a real-time WebSocket connection for receiving transcription updates
- **Parameters:** 
  - `clientID`: Valid UUID of the client
- **Notes:**
  - Implements ping/pong with 60-second timeout
  - Automatically disconnects on extended silence
  - Validates UUID format

## REST Endpoints

### `/api/clients`
- **Method:** GET
- **Description:** Lists all active clients and their most recent transcription from the current day
- **Response:** JSON map of client IDs to their latest TranscriptionMessage
- **Example Response:**
```json
{
    "client-uuid-1": {
        "timestamp": "2024-01-23T15:04:05Z",
        "text": "Latest transcription..."
    }
}
```

### `/api/clients/{clientID}`
- **Method:** GET
- **Description:** Retrieves the most recent transcription for a specific client from the current day
- **Parameters:**
  - `clientID`: UUID of the client
- **Response:** Single TranscriptionMessage object
- **Status Codes:**
  - 200: Success
  - 404: Client not found or no messages for today

### Static File Serving
- **Path:** `/`
- **Description:** Serves static files from the `scribe/static` directory
- **Note:** All non-API routes default to static file serving





# Development Notes:


Right now the client is what causes this to be as slow as it is from the Direct speech feel.
The reason for this is that the client establishes a background noise level Wait for a detection level VAD that will indicate if someone is speaking.
Then will happen is it will wait until there is a specific amount of time of silence before it then ships the data off to the server.
Inside the server right now there is some commented out code that will take one second snapshots of a buffer that's being built as it's been transmitted in,
And that makes it fast enough to wear it it looks like the user speech is coming right out.

But the problem is is that sometimes cuts up the person speech. The way that we could solve this is by having alternating frames on the server side such that we
Snip the the audio is as it's being transmitted in and then a half a second later we do it again so we have a staggered offset that's being done then when we have as it as it comes out we could then analyze the two streams and merge them back together correctly, essentially zipping them back up. The problem with this is that it takes a little intuition of what context what's going on potentially
I actually don't know I've never done this before, the way I intend solving this issue later is by by multiple client on the same device turn the internal of the application
 and then we'll just have a Tulpa who soul existence in this world will be to merge the streams and something coherent based on the context