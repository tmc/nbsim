import { useState, useEffect, useRef } from 'react'
import './App.css'

function App() {
  const [path] = useState(window.location.pathname);
  const [dest, setDest] = useState('http://localhost:8080/generated.html');
  const [fetching] = useState(false);
  const iframe = useRef() as React.MutableRefObject<HTMLIFrameElement>;

  useEffect(() => {
    console.log("useEffect fetching", fetching, path);
    if (fetching) return;
    console.log("fetching", path);
    const fetchfn = async () => {
      const o = await fetch('http://localhost:8080/_gen', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({ url: path}),
      });
      const data = await o.json();
      setDest(`http://localhost:8080/${data.url}`);
    };
    fetchfn().catch(console.error);
  }, [fetching, path]);

  return (
    <>
      <iframe
        id="if1"
        ref={iframe}
        style={{ width: '100%', height: '1024px', position: 'absolute'}}
        src={dest}
        title="gen1"
      />
    </>
  );
}


export default App
