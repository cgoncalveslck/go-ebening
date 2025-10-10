# go-api-ebening

A Discord bot for playing audio files in voice channels.

## Storage Modes

This bot supports two storage backends:

### Discord Channel Storage (Default)
Stores audio files as attachments in Discord channel messages. This is the default mode and requires no additional configuration.

### Railway Volume Storage
Stores audio files on a persistent Railway volume. This is recommended for production use with Railway.

To enable volume storage, set the following environment variables:

- `STORAGE_MODE=volume` - Enables volume storage
- `VOLUME_PATH=/data/sounds` - Path to the volume mount (default: `/data/sounds`)

For Railway deployment:
1. Create a volume in your Railway project
2. Mount it to `/data/sounds`
3. Set `STORAGE_MODE=volume` in your environment variables

## Environment Variables

- `BOT_TOKEN` (required) - Your Discord bot token
- `STORAGE_MODE` (optional) - Storage backend to use: `discord` (default) or `volume`
- `VOLUME_PATH` (optional) - Path for volume storage (default: `/data/sounds`)

## Railway Deployment

For Railway deployment with volumes:

1. Add a volume to your service
2. Mount the volume to `/data/sounds`
3. Set environment variables:
   - `BOT_TOKEN=your_bot_token`
   - `STORAGE_MODE=volume`

The bot will automatically use the volume for storing audio files instead of Discord messages.