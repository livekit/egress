import 'livekit-react/dist/index.css';
import React from 'react';
import 'react-aspect-ratio/aspect-ratio.css';
import {
  HashRouter as Router, Route, Switch,
} from 'react-router-dom';
import './App.css';
import HomePage from './HomePage';
import SpeakerPage from './SpeakerPage';

function App() {
  return (
    <div className="container">
      <Router>
        <Switch>
          <Route path="/speaker-light">
            <SpeakerPage interfaceStyle="light" />
          </Route>
          <Route path="/speaker-dark">
            <SpeakerPage interfaceStyle="dark" />
          </Route>
          <Route path="/" component={HomePage} />
        </Switch>
      </Router>
    </div>
  );
}

export default App;
