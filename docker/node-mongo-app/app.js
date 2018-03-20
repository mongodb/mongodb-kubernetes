//
// This is a simple node.js application which connects to mongodb, writes 3 json documents to the collection (db and
// collection will be created if they don't exist), and reads all the documents.
// May be extended to serve as a full web app accepting urls and commands and outputting result to the HTTP response
//

const MongoClient = require('mongodb').MongoClient;
const assert = require('assert');

// Database Name
const dbName = 'myproject';

// Insert function
const insertDocuments = function (db, callback) {
    // Get the documents collection
    const collection = db.collection('documents');
    // Insert some documents
    collection.insertMany([
        {a: 1}, {a: 2}, {a: 3}
    ], function (err, result) {
        assert.equal(err, null);
        assert.equal(3, result.result.n);
        assert.equal(3, result.ops.length);
        console.log("Inserted 3 documents into the collection");
        callback(result);
    });
}

const findDocuments = function (db, callback) {
    // Get the documents collection
    const collection = db.collection('documents');
    // Find some documents
    collection.find({}).toArray(function (err, docs) {
        assert.equal(err, null);
        console.log("Found the following records");
        console.log(docs)
        callback(docs);
    });
}

if (process.env.DATABASE_URL == null) {
    console.log("\"DATABASE_URL\" environment property is not specified!")
    process.exit(1)
}

url = process.env.DATABASE_URL;

// Use connect method to connect to the server
MongoClient.connect(url, function (err, client) {
    assert.equal(null, err);
    console.log("Connected successfully to server (" + url + ")");

    const db = client.db(dbName);

    db.collection("documents").drop(function (err, delOK) {
        if (err) throw err;
        if (delOK) console.log("Collection deleted");
    });

    insertDocuments(db, function () {
        findDocuments(db, function () {
            client.close();
        });
    });
});