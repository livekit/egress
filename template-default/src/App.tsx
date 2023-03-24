import '@livekit/components-styles';
import '@livekit/components-styles/prefabs';
import EgressHelper from '@livekit/egress-sdk';
import './App.css';
import RoomPage from './Room';

function App() {
  return (
    <div className="container">
      <RoomPage
        // EgressHelper retrieves parameters passed to the page
        url={EgressHelper.getLiveKitURL()}
        token={EgressHelper.getAccessToken()}
        layout={EgressHelper.getLayout()}
      />
    </div>
  );
}

export default App;
