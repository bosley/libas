git submodule update --init --recursive
cd whisper.cpp
make -j5
./models/download-ggml-model.sh medium.en-q5_0
./models/download-ggml-model.sh large-v3-turbo-q5_0

